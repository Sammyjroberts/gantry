import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { render, screen, fireEvent, cleanup, act } from "@testing-library/react";
import { ConnectPrompt } from "./ConnectPrompt";
import { getToken, clearToken } from "./token";
import { clearNeedsAuth, reportFetchStatus } from "./authGate";

const reloadMock = vi.fn();

beforeEach(() => {
  localStorage.clear();
  clearToken();
  clearNeedsAuth();
  reloadMock.mockClear();
  // jsdom's window.location.reload is non-configurable, so we can't redefine the
  // property directly. Replacing the whole location object IS allowed, so swap in
  // a copy whose reload is our spy (submit calls window.location.reload()).
  Object.defineProperty(window, "location", {
    configurable: true,
    value: { ...window.location, reload: reloadMock },
  });
});
afterEach(() => cleanup());

describe("ConnectPrompt", () => {
  it("is hidden until a 401 flips the gate on", () => {
    render(<ConnectPrompt />);
    expect(screen.queryByTestId("auth-token-input")).toBeNull();

    act(() => reportFetchStatus(401));
    expect(screen.getByTestId("auth-token-input")).toBeTruthy();
  });

  it("stores the submitted token and clears the gate on Connect", () => {
    render(<ConnectPrompt />);
    act(() => reportFetchStatus(401));

    const input = screen.getByTestId("auth-token-input") as HTMLInputElement;
    fireEvent.change(input, { target: { value: "  gtk_pasted_secret  " } });
    fireEvent.click(screen.getByText("Connect"));

    // Token stored (trimmed) and the prompt is dismissed (gate cleared).
    expect(getToken()).toBe("gtk_pasted_secret");
    expect(screen.queryByTestId("auth-token-input")).toBeNull();
    expect(reloadMock).toHaveBeenCalledTimes(1);
  });
});
