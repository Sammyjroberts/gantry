/** Centralized TanStack Query keys so invalidation stays consistent. */
export const qk = {
  catalog: ["catalog"] as const,
  workspaces: ["workspaces"] as const,
  workspace: (id: string) => ["workspace", id] as const,
  hardware: ["hardware"] as const,
  experiments: (deviceId: string) => ["experiments", deviceId] as const,
  models: ["models"] as const,
  cameras: ["cameras"] as const,
  tokens: ["tokens"] as const,
  sources: ["sources"] as const,
};
