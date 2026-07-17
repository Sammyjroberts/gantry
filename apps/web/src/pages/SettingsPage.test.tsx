import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, fireEvent, waitFor, cleanup } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { TokenInfo } from "@gantry/api-client";
import { SettingsPage } from "./SettingsPage";

function mkToken(over: Partial<TokenInfo>): TokenInfo {
  return {
    $typeName: "gantry.v1.TokenInfo",
    id: "",
    name: "",
    scopes: [],
    createdNs: 1_700_000_000_000_000_000n,
    lastUsedNs: 0n,
    ...over,
  } as TokenInfo;
}

// Mutable server-side fixtures the mocked client reads from / records into.
let rows: TokenInfo[] = [];
const client = {
  listTokens: vi.fn(async () => ({ tokens: rows })),
  createToken: vi.fn(async (req: { name: string; scopes: string[] }) => {
    const token = mkToken({ id: "new1", name: req.name, scopes: req.scopes });
    rows = [...rows, token];
    return { token, secret: "gtk_new1_supersecret" };
  }),
  deleteToken: vi.fn(async (req: { id: string }) => {
    rows = rows.filter((r) => r.id !== req.id);
    return {};
  }),
};

vi.mock("@gantry/api-client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@gantry/api-client")>();
  return { ...actual, createTokenClient: () => client };
});

function renderPage() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <SettingsPage />
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  rows = [];
  vi.clearAllMocks();
});
afterEach(() => cleanup());

describe("SettingsPage — access tokens", () => {
  it("shows the created secret exactly once and forgets it after dismiss", async () => {
    renderPage();

    // Open the form, name the token (default scope "read" is pre-checked).
    fireEvent.click(screen.getByText("New token"));
    fireEvent.change(screen.getByTestId("token-name"), { target: { value: "ci-runner" } });
    fireEvent.click(screen.getByText("Create token"));

    // The one-time secret is revealed.
    const secret = await screen.findByTestId("token-secret");
    expect(secret.textContent).toContain("gtk_new1_supersecret");
    expect(secret.textContent).toContain("won't see this again");
    expect(client.createToken).toHaveBeenCalledWith({ name: "ci-runner", scopes: ["read"] });

    // Dismiss → the secret is gone from the DOM (only lived in component state).
    fireEvent.click(screen.getByTitle("dismiss"));
    expect(screen.queryByTestId("token-secret")).toBeNull();
  });

  it("revokes a token (confirm → deleteToken) ", async () => {
    rows = [mkToken({ id: "tok-42", name: "old-key", scopes: ["read", "operate"] })];
    renderPage();

    await screen.findByText("old-key");

    // First click asks to confirm; second click revokes.
    fireEvent.click(screen.getByTestId("token-revoke-tok-42"));
    fireEvent.click(screen.getByTestId("token-confirm-tok-42"));

    await waitFor(() => expect(client.deleteToken).toHaveBeenCalledWith({ id: "tok-42" }));
  });

  it("renders scopes as chips and 'never' for an unused token", async () => {
    rows = [mkToken({ id: "t1", name: "k", scopes: ["admin"], lastUsedNs: 0n })];
    renderPage();

    await screen.findByText("k");
    expect(screen.getByText("admin")).toBeTruthy(); // chip
    expect(screen.getAllByText("never").length).toBeGreaterThan(0);
  });
});
