import { useMemo } from "react";
import { resolveBaseUrl } from "../config";
import { useExperiments } from "../useExperiments";
import { WorkspaceDataProvider } from "../live/WorkspaceData";
import { TimeStrips } from "../components/TimeStrips";
import { isRunning } from "../experiments";

function fmtTime(ns: bigint): string {
  if (ns === 0n) return "—";
  return new Date(Number(ns) / 1e6).toLocaleString();
}
function fmtDuration(startNs: bigint, endNs: bigint): string {
  const end = endNs === 0n ? Date.now() * 1e6 : Number(endNs);
  const sec = Math.max(0, (end - Number(startNs)) / 1e9);
  if (sec < 60) return `${sec.toFixed(0)}s`;
  if (sec < 3600) return `${(sec / 60).toFixed(1)}m`;
  return `${(sec / 3600).toFixed(1)}h`;
}

/**
 * The Experiments page. Hosts the global strips (so replay drives whatever
 * workspace is loaded) plus the experiment management bar and a run table.
 */
export function ExperimentsPage() {
  const baseUrl = useMemo(resolveBaseUrl, []);
  const exp = useExperiments({ baseUrl, deviceId: "", pollMs: 5000 });

  return (
    <WorkspaceDataProvider experiments={exp.experiments}>
      <div className="page experiments-page">
        <TimeStrips exp={exp} />
        <div className="page-body">
          <h1 className="page-title">Experiments</h1>
          {exp.experiments.length === 0 ? (
            <div className="page-empty">No experiments yet. Start one from the bar above.</div>
          ) : (
            <table className="exp-table">
              <thead>
                <tr>
                  <th>Name</th>
                  <th>Status</th>
                  <th>Started</th>
                  <th>Duration</th>
                  <th>Notes</th>
                </tr>
              </thead>
              <tbody>
                {exp.experiments.map((e) => (
                  <tr key={e.id} className={isRunning(e) ? "is-running" : ""}>
                    <td>{e.name}</td>
                    <td>
                      <span className={`exp-badge ${isRunning(e) ? "running" : "done"}`}>
                        {isRunning(e) ? "running" : "done"}
                      </span>
                    </td>
                    <td>{fmtTime(e.startNs)}</td>
                    <td>{fmtDuration(e.startNs, e.endNs)}</td>
                    <td className="exp-notes">{e.notes}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
      </div>
    </WorkspaceDataProvider>
  );
}
