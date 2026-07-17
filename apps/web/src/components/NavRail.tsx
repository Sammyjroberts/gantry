import { useState } from "react";
import { NavLink } from "react-router-dom";
import {
  LayoutGrid,
  Cpu,
  FlaskConical,
  Database,
  Settings,
  PanelLeftClose,
  PanelLeftOpen,
} from "lucide-react";
import { useLive } from "../live/LiveContext";

const NAV = [
  { to: "/", label: "Workspace", Icon: LayoutGrid, end: true },
  { to: "/hardware", label: "Hardware", Icon: Cpu, end: false },
  { to: "/experiments", label: "Experiments", Icon: FlaskConical, end: false },
  { to: "/data", label: "Data", Icon: Database, end: false },
] as const;

/**
 * The left navigation rail: the four pages plus a bottom status cluster
 * (connection state + frames/sec) fed by the app-level live subscription.
 * Collapsible to an icon strip.
 */
export function NavRail() {
  const [collapsed, setCollapsed] = useState(false);
  const { status } = useLive();

  return (
    <nav className={`nav-rail ${collapsed ? "is-collapsed" : ""}`}>
      <div className="nav-brand">
        <span className="brand-mark">▚</span>
        {!collapsed && <span className="nav-brand-text">GANTRY</span>}
      </div>

      <div className="nav-links">
        {NAV.map(({ to, label, Icon, end }) => (
          <NavLink
            key={to}
            to={to}
            end={end}
            className={({ isActive }) => `nav-link ${isActive ? "is-active" : ""}`}
            title={label}
            data-testid={`nav-${label.toLowerCase()}`}
          >
            <Icon size={18} className="nav-icon" aria-hidden />
            {!collapsed && <span className="nav-label">{label}</span>}
          </NavLink>
        ))}
      </div>

      {/* Settings sits apart from the four main pages, above the status cluster. */}
      <NavLink
        to="/settings"
        className={({ isActive }) => `nav-link nav-link--settings ${isActive ? "is-active" : ""}`}
        title="Settings"
        data-testid="nav-settings"
      >
        <Settings size={18} className="nav-icon" aria-hidden />
        {!collapsed && <span className="nav-label">Settings</span>}
      </NavLink>

      <div className="nav-foot">
        <div
          className={`nav-conn conn-${status.conn}`}
          title={`connection: ${status.conn}${status.lastError ? ` — ${status.lastError}` : ""}`}
          data-testid="conn-status"
        >
          <span className="conn-dot" />
          {!collapsed && <span className="nav-conn-label">{status.conn.toUpperCase()}</span>}
        </div>
        {!collapsed && (
          <div className="nav-fps" title="frames per second">
            <span className="nav-fps-v">{status.fps}</span>
            <span className="nav-fps-k">fps</span>
          </div>
        )}
        <button
          className="nav-collapse"
          onClick={() => setCollapsed((v) => !v)}
          title={collapsed ? "expand" : "collapse"}
        >
          {collapsed ? <PanelLeftOpen size={16} /> : <PanelLeftClose size={16} />}
        </button>
      </div>
    </nav>
  );
}
