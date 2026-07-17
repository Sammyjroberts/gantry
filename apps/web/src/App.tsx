import { useState } from "react";
import {
  BrowserRouter,
  Navigate,
  Outlet,
  Route,
  Routes,
} from "react-router-dom";
import { QueryClientProvider } from "@tanstack/react-query";
import { createConsoleQueryClient } from "./query/client";
import { LiveProvider, useLive } from "./live/LiveContext";
import { NavRail } from "./components/NavRail";
import { StatusBar } from "./components/StatusBar";
import { WorkspacePage } from "./pages/WorkspacePage";
import { HardwareListPage } from "./pages/HardwareListPage";
import { HardwareDetailPage } from "./pages/HardwareDetailPage";
import { ExperimentsPage } from "./pages/ExperimentsPage";
import { DataPage } from "./pages/DataPage";
import { SettingsPage } from "./pages/SettingsPage";
import { ConnectPrompt } from "./auth/ConnectPrompt";
import { useTimeStore } from "./store/timeStore";
import { useClockDriver } from "./store/clock";

/** The app shell: nav rail + routed outlet + a slim status footer. */
function AppShell() {
  const paused = useTimeStore((s) => s.paused);
  const replayPlaying = useTimeStore((s) => s.replay?.clock.playing ?? false);
  useClockDriver(!paused || replayPlaying);
  const { status, subscribedKeys } = useLive();

  return (
    <div className="app app-shell">
      <div className="app-shell-body">
        <NavRail />
        <main className="app-main">
          <Outlet />
        </main>
      </div>
      <StatusBar
        conn={status.conn}
        fps={status.fps}
        droppedLate={status.droppedLate}
        reconnects={status.reconnects}
        channelCount={subscribedKeys.length}
        lastError={status.lastError}
      />
    </div>
  );
}

export function App() {
  const [queryClient] = useState(createConsoleQueryClient);

  return (
    <QueryClientProvider client={queryClient}>
      <BrowserRouter>
        <LiveProvider>
          <Routes>
            <Route element={<AppShell />}>
              <Route path="/" element={<WorkspacePage />} />
              <Route path="/hardware" element={<HardwareListPage />} />
              <Route path="/hardware/:deviceId" element={<HardwareDetailPage />} />
              <Route path="/experiments" element={<ExperimentsPage />} />
              <Route path="/data" element={<DataPage />} />
              <Route path="/settings" element={<SettingsPage />} />
              <Route path="*" element={<Navigate to="/" replace />} />
            </Route>
          </Routes>
          {/* Global "connect to bench" gate. Localhost never triggers it: the
              Bench trusts loopback and never returns 401, so it stays hidden. */}
          <ConnectPrompt />
        </LiveProvider>
      </BrowserRouter>
    </QueryClientProvider>
  );
}
