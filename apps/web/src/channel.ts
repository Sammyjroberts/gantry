import type { ChannelInfo } from "@gantry/api-client";

/**
 * A channel's identity. As of proto v1, a channel is identified by the pair
 * (packet, name) — NOT by name alone. Two packets may expose the same param
 * name with different kinds (e.g. imu.temp:f64 vs power.temp:i64), so keying on
 * name would collide their selection/buffer/series state.
 *
 * `packet` is empty for ad-hoc channels.
 */
export interface ChannelId {
  packet: string;
  name: string;
}

// Unit separator (U+001F): a control char that never appears in a channel name
// (names are lowercase dotted tokens) or packet token, so the join is
// unambiguous. A "." join would be lossy — {packet:"a", name:"b.c"} and
// {packet:"a.b", name:"c"} would both render "a.b.c" and collide.
const SEP = "\u001f";

/**
 * Canonical, collision-free key for a (packet, name) identity. Used as the
 * selection-set key, the timeseries buffer key, and the chart series key.
 */
export function channelKey(packet: string, name: string): string {
  return `${packet}${SEP}${name}`;
}

/** {@link channelKey} for a {@link ChannelId}. */
export function idKey(id: ChannelId): string {
  return channelKey(id.packet, id.name);
}

/** {@link channelKey} for a catalogue {@link ChannelInfo}. */
export function infoKey(info: ChannelInfo): string {
  return channelKey(info.packet, info.name);
}

/** Recover the (packet, name) identity from a canonical key. */
export function parseKey(key: string): ChannelId {
  const i = key.indexOf(SEP);
  if (i < 0) return { packet: "", name: key };
  return { packet: key.slice(0, i), name: key.slice(i + 1) };
}

/**
 * Human-facing label: `packet.name` for packeted channels, bare `name` for
 * ad-hoc (empty packet). This is display-only; never use it as a key (it is
 * lossy — see {@link channelKey}).
 */
export function channelLabel(packet: string, name: string): string {
  return packet ? `${packet}.${name}` : name;
}

/** A packet and its channels, for the grouped sidebar. */
export interface PacketGroup {
  /** Packet name; "" is the ad-hoc bucket. */
  packet: string;
  /** True when this is the ad-hoc (empty-packet) bucket. */
  adHoc: boolean;
  channels: ChannelInfo[];
}

/**
 * Group a device's channels by packet for the sidebar tree. Packeted groups
 * come first (sorted by packet name); the ad-hoc bucket (empty packet), if any,
 * sorts last. Channel order within a group is preserved from the catalogue.
 */
export function groupByPacket(channels: ChannelInfo[]): PacketGroup[] {
  const byPacket = new Map<string, ChannelInfo[]>();
  for (const ch of channels) {
    const list = byPacket.get(ch.packet);
    if (list) list.push(ch);
    else byPacket.set(ch.packet, [ch]);
  }
  const packets = [...byPacket.keys()].sort((a, b) => {
    if (a === "") return 1; // ad-hoc bucket last
    if (b === "") return -1;
    return a < b ? -1 : a > b ? 1 : 0;
  });
  return packets.map((packet) => ({
    packet,
    adHoc: packet === "",
    channels: byPacket.get(packet)!,
  }));
}

/**
 * The distinct channel NAMES to request from LiveService.Subscribe for a set of
 * selected keys. The server routes by channel name (packet is frame metadata,
 * not a subject token — see core/go/stream/subject.go), so the request carries
 * names; returning frames are re-keyed by (packet, name) on the client.
 */
export function subscribeNames(selectedKeys: Iterable<string>): string[] {
  const names = new Set<string>();
  for (const key of selectedKeys) names.add(parseKey(key).name);
  return [...names];
}
