#!/usr/bin/env python3
"""Serial-port discovery, leader/follower pairing, and persistence for SO-101.

No hardware or pyserial import happens at module load — ``serial`` is imported
lazily inside the functions that enumerate ports, so this module (and the tests
that exercise its pure logic) run on a machine with nothing plugged in.

Feetech's SO-10x USB adapters are generic USB-serial bridges. We recognise the
common ones by USB VID:PID so a zero-argument run can pick them out from
unrelated COM ports (Bluetooth, motherboard UARTs). lerobot's own ``find_port``
does *not* filter by VID:PID at all -- it diffs the whole port list across an
unplug event -- so we use the same unplug-diff for the leader/follower *pairing*
step and treat the VID:PID list purely as a candidate filter.
"""

from __future__ import annotations

import json
import os

# USB VID:PID of the serial bridges shipped on / commonly used with SO-10x kits.
# WCH CH340 / CH343 / CH9102, Silicon Labs CP210x, and FTDI. Values are the
# well-known published ids for these chips.
KNOWN_ADAPTERS: set[tuple[int, int]] = {
    (0x1A86, 0x7523),  # WCH CH340
    (0x1A86, 0x5523),  # WCH CH341 (uart mode)
    (0x1A86, 0x55D3),  # WCH CH343 / CH9102 (as used on newer SO-101 boards)
    (0x1A86, 0x55D4),  # WCH CH9102F
    (0x10C4, 0xEA60),  # Silicon Labs CP2102/CP210x
    (0x10C4, 0xEA70),  # Silicon Labs CP2105 (dual)
    (0x0403, 0x6001),  # FTDI FT232R
    (0x0403, 0x6014),  # FTDI FT232H
    (0x0403, 0x6015),  # FTDI FT-X
}

DEFAULT_CACHE = os.path.join(os.path.dirname(os.path.abspath(__file__)), ".so101_ports.json")


def is_known_adapter(vid, pid) -> bool:
    if vid is None or pid is None:
        return False
    return (int(vid), int(pid)) in KNOWN_ADAPTERS


def list_candidate_ports() -> list[str]:
    """Device names of attached USB-serial adapters that look like Feetech
    bridges, sorted for stable ordering. Empty when nothing recognisable is
    attached (that is the no-hardware signal the CLI reports on)."""
    from serial.tools import list_ports  # lazy: only needed when hardware is present

    out = [p.device for p in list_ports.comports() if is_known_adapter(p.vid, p.pid)]
    return sorted(out)


def describe_ports() -> list[str]:
    """Human-readable 'COM4  (CH340, VID:PID=1A86:7523)' lines for every serial
    port on the machine -- used in the 'no adapters found' guidance so the user
    can see what *is* attached."""
    from serial.tools import list_ports

    lines = []
    for p in list_ports.comports():
        vid = f"{p.vid:04X}" if p.vid is not None else "----"
        pid = f"{p.pid:04X}" if p.pid is not None else "----"
        mark = "  <- looks like a Feetech adapter" if is_known_adapter(p.vid, p.pid) else ""
        lines.append(f"{p.device}  ({p.description}, VID:PID={vid}:{pid}){mark}")
    return lines


# ---------------------------------------------------------------------------
# Persistence: port -> role, remembered between runs
# ---------------------------------------------------------------------------


def load_port_map(path: str = DEFAULT_CACHE) -> dict:
    """Return {'leader': 'COM4', 'follower': 'COM5'} or {} if absent/corrupt."""
    try:
        with open(path, "r", encoding="utf-8") as fh:
            data = json.load(fh)
    except (OSError, ValueError):
        return {}
    if not isinstance(data, dict):
        return {}
    return {k: v for k, v in data.items() if k in ("leader", "follower") and isinstance(v, str)}


def save_port_map(mapping: dict, path: str = DEFAULT_CACHE) -> None:
    clean = {k: mapping[k] for k in ("leader", "follower") if mapping.get(k)}
    with open(path, "w", encoding="utf-8") as fh:
        json.dump(clean, fh, indent=2, sort_keys=True)
        fh.write("\n")


# ---------------------------------------------------------------------------
# Interactive one-time pairing (lerobot find_port style)
# ---------------------------------------------------------------------------


class NoAdaptersFound(Exception):
    """Zero recognisable USB-serial adapters attached."""


class PairingFailed(Exception):
    """The unplug-diff did not isolate exactly one port."""


def interactive_pair(list_ports_fn=list_candidate_ports, input_fn=input, print_fn=print) -> dict:
    """Ask the user to unplug the leader; the port that disappears is the
    leader, the survivor is the follower. Mirrors lerobot's find_port UX.

    Returns {'leader': <port>, 'follower': <port>}. Injectable I/O + port
    enumeration keep it unit-testable with no hardware.
    """
    before = list(list_ports_fn())
    if len(before) < 2:
        raise PairingFailed(f"need two adapters to pair, saw {before}")
    print_fn(f"Found two SO-101 adapters: {', '.join(before)}")
    print_fn("Unplug the LEADER arm's USB cable now, then press Enter...")
    input_fn()
    after = list(list_ports_fn())
    gone = [p for p in before if p not in after]
    if len(gone) != 1:
        raise PairingFailed(
            f"expected exactly one port to disappear, but {before} -> {after} "
            f"(disappeared: {gone}). Plug everything back in and retry."
        )
    leader = gone[0]
    follower = next(p for p in before if p != leader)
    print_fn(f"Leader = {leader}, follower = {follower}. Plug the leader back in.")
    return {"leader": leader, "follower": follower}


def resolve_ports(
    leader=None,
    follower=None,
    cache_path: str = DEFAULT_CACHE,
    interactive: bool = True,
    list_ports_fn=list_candidate_ports,
    input_fn=input,
    print_fn=print,
) -> dict:
    """Turn CLI intent into a concrete {'leader': port|None, 'follower': port|None}.

    Precedence:
      1. Explicit --leader/--follower always win and short-circuit detection.
      2. Otherwise, if a persisted mapping still points at attached adapters,
         reuse it (no prompts).
      3. Otherwise, with exactly two candidates, run the interactive pairing and
         persist the result.
      4. Zero candidates -> NoAdaptersFound (the CLI turns this into friendly
         guidance). One, or more-than-two, candidates that we cannot
         disambiguate -> PairingFailed with advice to pass --leader/--follower.
    """
    # 1. Any explicit override means the user is driving; don't auto-detect.
    if leader or follower:
        return {"leader": leader, "follower": follower}

    candidates = list(list_ports_fn())
    if not candidates:
        raise NoAdaptersFound()

    # 2. Reuse a persisted mapping when both remembered ports are still present.
    cached = load_port_map(cache_path)
    if cached.get("leader") in candidates and cached.get("follower") in candidates:
        print_fn(f"Using remembered ports: leader={cached['leader']} follower={cached['follower']}")
        return {"leader": cached["leader"], "follower": cached["follower"]}

    # 3. Exactly two -> pair interactively and remember.
    if len(candidates) == 2:
        if not interactive:
            raise PairingFailed(
                f"two adapters {candidates} but no saved pairing; run once "
                f"interactively or pass --leader/--follower"
            )
        mapping = interactive_pair(list_ports_fn, input_fn, print_fn)
        save_port_map(mapping, cache_path)
        return mapping

    # 4. One (role unknown) or >2 (ambiguous): let the user say which is which.
    raise PairingFailed(
        f"found {len(candidates)} adapter(s): {candidates}. "
        f"Pass --leader/--follower explicitly to say which is which."
    )
