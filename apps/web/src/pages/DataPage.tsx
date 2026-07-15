import { Suspense, lazy, useMemo } from "react";
import { resolveBaseUrl } from "../config";

const SqlConsole = lazy(() => import("../SqlConsole"));

/** The Data page — a full-height DuckDB SQL console. */
export function DataPage() {
  const baseUrl = useMemo(resolveBaseUrl, []);
  return (
    <div className="page data-page">
      <Suspense fallback={<div className="scene3d-loading">loading SQL module…</div>}>
        <SqlConsole baseUrl={baseUrl} onClose={() => {}} />
      </Suspense>
    </div>
  );
}
