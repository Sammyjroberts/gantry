import { useMemo } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  createTokenClient,
  type CreateTokenResponse,
  type TokenInfo,
} from "@gantry/api-client";
import { resolveBaseUrl } from "../config";
import { apiClientOptions } from "../auth/authGate";
import { qk } from "./keys";

/**
 * Access-token admin queries for Settings → Access tokens. Mirrors the
 * useWorkspaces pattern (one client per call, react-query cache + invalidation).
 * Managing tokens requires loopback OR the `admin` scope; the server enforces
 * that and returns PermissionDenied otherwise (surfaced as the query error).
 */

/** List token metadata (secrets never returned here). Newest-created first. */
export function useTokenList() {
  const baseUrl = useMemo(resolveBaseUrl, []);
  return useQuery({
    queryKey: qk.tokens,
    queryFn: async ({ signal }): Promise<TokenInfo[]> => {
      const client = createTokenClient(baseUrl, apiClientOptions());
      const res = await client.listTokens({}, { signal });
      return [...res.tokens].sort((a, b) => Number(b.createdNs - a.createdNs));
    },
    staleTime: 10_000,
  });
}

export interface CreateTokenArgs {
  name: string;
  scopes: string[];
}

/**
 * Create a token. The response carries the full secret exactly ONCE — the caller
 * must show it immediately (see SettingsPage). The list is invalidated so the
 * new row appears.
 */
export function useCreateToken() {
  const baseUrl = useMemo(resolveBaseUrl, []);
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (args: CreateTokenArgs): Promise<CreateTokenResponse> => {
      const client = createTokenClient(baseUrl, apiClientOptions());
      return client.createToken({ name: args.name, scopes: args.scopes });
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: qk.tokens });
    },
  });
}

/** Revoke a token immediately by id. */
export function useDeleteToken() {
  const baseUrl = useMemo(resolveBaseUrl, []);
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (id: string): Promise<void> => {
      const client = createTokenClient(baseUrl, apiClientOptions());
      await client.deleteToken({ id });
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: qk.tokens });
    },
  });
}
