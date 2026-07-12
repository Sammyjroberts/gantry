import type { ConnState } from "../useLiveStream";

export interface StatusBarProps {
  conn: ConnState;
  fps: number;
  droppedLate: number;
  reconnects: number;
  channelCount: number;
  lastError: string | null;
}

const CONN_LABEL: Record<ConnState, string> = {
  idle: "IDLE",
  connecting: "CONNECTING",
  live: "LIVE",
  reconnecting: "RECONNECTING",
  error: "ERROR",
};

export function StatusBar({
  conn,
  fps,
  droppedLate,
  reconnects,
  channelCount,
  lastError,
}: StatusBarProps) {
  return (
    <footer className="statusbar">
      <span className={`conn-pill conn-${conn}`}>
        <span className="conn-dot" />
        {CONN_LABEL[conn]}
      </span>
      <span className="stat">
        <span className="stat-k">frames/s</span>
        <span className="stat-v">{fps}</span>
      </span>
      <span className="stat">
        <span className="stat-k">dropped-late</span>
        <span className={`stat-v ${droppedLate > 0 ? "stat-warn" : ""}`}>{droppedLate}</span>
      </span>
      <span className="stat">
        <span className="stat-k">reconnects</span>
        <span className="stat-v">{reconnects}</span>
      </span>
      <span className="stat">
        <span className="stat-k">channels</span>
        <span className="stat-v">{channelCount}</span>
      </span>
      {lastError && conn !== "live" && (
        <span className="stat stat-err" title={lastError}>
          {lastError}
        </span>
      )}
    </footer>
  );
}
