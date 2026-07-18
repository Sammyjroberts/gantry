import { useMemo } from "react";
import { Link } from "react-router-dom";
import { Cpu, CircleAlert, ChevronRight } from "lucide-react";
import { resolveBaseUrl } from "../config";
import { useHardware } from "../useHardware";
import { SourcesCard } from "../components/SourcesCard";

/**
 * The hardware list page: configured device cards (linking to their detail /
 * configuration page) plus the "seen but unconfigured" devices, each promotable
 * by opening its detail page and saving.
 */
export function HardwareListPage() {
  const baseUrl = useMemo(resolveBaseUrl, []);
  const hw = useHardware({ baseUrl });

  return (
    <div className="page hardware-page">
      <div className="page-body">
        <h1 className="page-title">Hardware</h1>
        {hw.error && <div className="page-error">{hw.error}</div>}

        {/* Telemetry sources feed hardware: the bench-managed in-process clients
            that pull external telemetry in (Foxglove today). Above the device list
            because a source is what makes a device appear here. */}
        <SourcesCard />

        <h2 className="hw-section">Configured</h2>
        {hw.hardware.length === 0 && <div className="page-empty">No configured devices yet.</div>}
        <div className="hw-cards">
          {hw.hardware.map((h) => (
            <Link key={h.deviceId} to={`/hardware/${encodeURIComponent(h.deviceId)}`} className="hw-card">
              <Cpu size={18} className="hw-card-icon" />
              <div className="hw-card-body">
                <div className="hw-card-name">{h.displayName || h.deviceId}</div>
                <div className="hw-card-id">{h.deviceId}</div>
                {h.description && <div className="hw-card-desc">{h.description}</div>}
              </div>
              <ChevronRight size={16} className="hw-card-chevron" />
            </Link>
          ))}
        </div>

        {hw.unconfigured.length > 0 && (
          <>
            <h2 className="hw-section">Seen — unconfigured</h2>
            <div className="hw-cards">
              {hw.unconfigured.map((id) => (
                <Link key={id} to={`/hardware/${encodeURIComponent(id)}`} className="hw-card is-unconfigured">
                  <CircleAlert size={18} className="hw-card-icon" />
                  <div className="hw-card-body">
                    <div className="hw-card-name">{id}</div>
                    <div className="hw-card-id">emitting telemetry — click to configure</div>
                  </div>
                  <ChevronRight size={16} className="hw-card-chevron" />
                </Link>
              ))}
            </div>
          </>
        )}
      </div>
    </div>
  );
}
