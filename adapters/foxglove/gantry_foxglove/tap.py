"""The tap: connect to a Foxglove WebSocket server, map, and publish to Gantry.

One long-lived coroutine (:meth:`FoxgloveTap.run`) that:

* connects with the ``foxglove.websocket.v1`` subprotocol and reconnects with
  exponential backoff when the server drops or restarts;
* tracks advertised channels dynamically (advertise / unadvertise), subscribing
  to every channel a mapping rule matches (image topics only when the video tee
  is live);
* decodes binary MessageData frames, maps scalar payloads to Gantry frames, and
  routes image payloads (foxglove CompressedImage JPEG) to the video tee;
* batches frames per device and flushes on a small time/size cadence.

The heavy/blocking work (HTTP ingest POSTs, video encode) is kept off the event
loop: ingest flushes run in a thread executor, video encode on its own thread.
"""

from __future__ import annotations

import asyncio
import json
import logging

import websockets
from websockets.asyncio.client import connect

from . import protocol as fp
from .mapping import Mapping
from .publisher import PublisherPool
from .video import VideoTee

log = logging.getLogger("gantry_foxglove.tap")


class Backoff:
    """Exponential backoff with a cap (same shape as the SO-101 bridge)."""

    def __init__(self, start: float = 0.5, cap: float = 5.0):
        self.start = start
        self.cap = cap
        self._cur = start

    def next(self) -> float:
        v = self._cur
        self._cur = min(self._cur * 2, self.cap)
        return v

    def reset(self) -> None:
        self._cur = self.start


class FoxgloveTap:
    def __init__(
        self,
        url: str,
        mapping: Mapping,
        pool: PublisherPool,
        video: VideoTee | None = None,
        *,
        flush_interval: float = 0.1,
        flush_max_frames: int = 500,
    ):
        self.url = url
        self.mapping = mapping
        self.pool = pool
        self.video = video
        self.flush_interval = flush_interval
        self.flush_max_frames = flush_max_frames
        self._stop = asyncio.Event()
        # Per-connection state (reset on each (re)connect).
        self._channels: dict[int, fp.Channel] = {}  # channel id -> Channel
        self._subs: dict[int, fp.Channel] = {}  # subscription id -> Channel
        self._chan_to_sub: dict[int, int] = {}  # channel id -> subscription id
        self._next_sub = 1
        # Stats.
        self.connects = 0
        self.messages = 0
        self.mapped_frames = 0

    def stop(self) -> None:
        self._stop.set()

    async def run(self) -> None:
        """Connect/subscribe/pump with reconnect until :meth:`stop`."""
        backoff = Backoff()
        if self.video is not None and self.video.enabled:
            self.video.start()
        try:
            while not self._stop.is_set():
                try:
                    await self._session()
                    backoff.reset()
                except asyncio.CancelledError:
                    raise
                except (OSError, websockets.exceptions.WebSocketException) as e:
                    log.warning("foxglove: connection lost (%s); reconnecting", e)
                except Exception as e:  # noqa: BLE001 - keep the tap alive
                    log.exception("foxglove: session error (%s); reconnecting", e)
                if self._stop.is_set():
                    break
                try:
                    await asyncio.wait_for(self._stop.wait(), timeout=backoff.next())
                except asyncio.TimeoutError:
                    pass
        finally:
            await self._flush_now()
            if self.video is not None:
                self.video.close()

    async def _session(self) -> None:
        self._reset_connection_state()
        async with connect(self.url, subprotocols=[fp.SUBPROTOCOL], max_size=None) as ws:
            if ws.subprotocol != fp.SUBPROTOCOL:
                raise ConnectionError(
                    f"server did not select {fp.SUBPROTOCOL!r} (got {ws.subprotocol!r})"
                )
            self.connects += 1
            log.info("foxglove: connected to %s", self.url)
            flusher = asyncio.create_task(self._flush_loop())
            # A stop request must break the receive loop even mid-session: this
            # task closes the socket when stop() is called, ending `async for`.
            closer = asyncio.create_task(self._close_on_stop(ws))
            try:
                async for message in ws:
                    if isinstance(message, str):
                        await self._on_json(ws, message)
                    else:
                        self._on_binary(message)
            finally:
                for task in (flusher, closer):
                    task.cancel()
                    try:
                        await task
                    except asyncio.CancelledError:
                        pass

    async def _close_on_stop(self, ws) -> None:
        await self._stop.wait()
        await ws.close()

    def _reset_connection_state(self) -> None:
        self._channels.clear()
        self._subs.clear()
        self._chan_to_sub.clear()
        self._next_sub = 1

    async def _on_json(self, ws, text: str) -> None:
        try:
            obj = fp.decode_json(text)
        except ValueError as e:
            log.debug("foxglove: bad JSON control message: %s", e)
            return
        op = obj.get("op")
        if op == fp.ADVERTISE:
            await self._on_advertise(ws, fp.parse_advertise(obj))
        elif op == fp.UNADVERTISE:
            self._on_unadvertise(fp.parse_unadvertise(obj))
        elif op == fp.SERVER_INFO:
            log.info("foxglove: serverInfo name=%s capabilities=%s",
                     obj.get("name"), obj.get("capabilities"))
        elif op == fp.STATUS:
            log.info("foxglove: status[%s] %s", obj.get("level"), obj.get("message"))

    async def _on_advertise(self, ws, channels) -> None:
        new_subs = []
        for ch in channels:
            self._channels[ch.id] = ch
            if ch.id in self._chan_to_sub:
                continue
            rule = self.mapping.match(ch.topic)
            if rule is None:
                continue
            if rule.kind == "image" and (self.video is None or not self.video.enabled):
                continue  # no point subscribing to images with no tee
            sub_id = self._next_sub
            self._next_sub += 1
            self._subs[sub_id] = ch
            self._chan_to_sub[ch.id] = sub_id
            new_subs.append((sub_id, ch.id))
            log.info("foxglove: subscribing sub=%d channel=%d topic=%s (%s)",
                     sub_id, ch.id, ch.topic, rule.kind)
        if new_subs:
            await ws.send(fp.encode_subscribe(new_subs))

    def _on_unadvertise(self, channel_ids) -> None:
        for cid in channel_ids:
            self._channels.pop(cid, None)
            sub = self._chan_to_sub.pop(cid, None)
            if sub is not None:
                self._subs.pop(sub, None)
                log.info("foxglove: channel %d unadvertised (sub %d dropped)", cid, sub)

    def _on_binary(self, data: bytes) -> None:
        try:
            msg = fp.decode_binary(data)
        except ValueError as e:
            log.debug("foxglove: bad binary frame: %s", e)
            return
        if not isinstance(msg, fp.MessageData):
            return  # Time frames etc. are not needed by the tap
        self.messages += 1
        ch = self._subs.get(msg.subscription_id)
        if ch is None:
            return
        rule = self.mapping.match(ch.topic)
        if rule is None:
            return
        if rule.kind == "image":
            self._handle_image(rule, ch, msg)
        else:
            self._handle_scalars(rule, msg)

    def _handle_scalars(self, rule, msg: fp.MessageData) -> None:
        try:
            payload = json.loads(msg.payload)
        except Exception:
            return
        scalars = payload.get(rule.scalars_field) if isinstance(payload, dict) else None
        if not isinstance(scalars, list):
            return
        for device, packet, channel, value, t_ns in self.mapping.map_scalars(rule, scalars, msg.log_time):
            pub = self.pool.get(device)
            # Unit comes from the emit target; look it up once per (packet,channel).
            pub.ensure_channel(packet, channel, self._unit_for(rule, device, channel))
            pub.add(packet, channel, value, t_ns)
            self.mapped_frames += 1

    def _unit_for(self, rule, device: str, channel: str) -> str | None:
        for em in rule.emit:
            if em.device == device and (em.channel == channel or em.channel_from_label):
                return em.unit
        # track_err's unit.
        te = self.mapping.track_err
        if te is not None and device == te.device and channel == te.out_channel:
            return te.unit
        return None

    def _handle_image(self, rule, ch, msg: fp.MessageData) -> None:
        if self.video is None or not self.video.enabled:
            return
        data, fmt = fp.extract_compressed_image(msg.payload)
        if not data:
            return
        if fmt and fmt.lower() not in ("jpeg", "jpg"):
            log.debug("foxglove: skipping non-jpeg image (%s) on %s", fmt, ch.topic)
            return
        camera = self.mapping.camera_for(rule, ch.topic)
        self.video.feed(camera, data, msg.log_time)

    async def _flush_loop(self) -> None:
        while True:
            await asyncio.sleep(self.flush_interval)
            if self.pool.pending() > 0:
                await self._flush_now()

    async def _flush_now(self) -> None:
        await asyncio.get_running_loop().run_in_executor(None, self.pool.flush_all)
