import { QueryClient } from "@tanstack/react-query";

/**
 * The console's TanStack Query client. Server state (workspaces, hardware,
 * experiments, the channel catalogue, video cameras, model files) all flows
 * through here — replacing hand-rolled polling with cache + invalidation.
 *
 * Defaults lean live-bench: a short staleTime so a returning tab refreshes, no
 * refetch-on-window-focus spam (the bench is long-lived), and one retry (RPCs
 * fail fast and the UI surfaces errors). Per-query `refetchInterval` opts in to
 * polling only where liveness matters (the catalogue, camera list).
 */
export function createConsoleQueryClient(): QueryClient {
  return new QueryClient({
    defaultOptions: {
      queries: {
        staleTime: 5_000,
        gcTime: 5 * 60_000,
        retry: 1,
        refetchOnWindowFocus: false,
      },
      mutations: {
        retry: 0,
      },
    },
  });
}
