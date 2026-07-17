import { describe, it, expect, beforeEach } from "vitest";
import { ConnectError, Code } from "@gantry/api-client";
import { getToken, setToken, clearToken, authHeaders, withToken } from "./token";
import {
  getNeedsAuth,
  clearNeedsAuth,
  isAuthChallenge,
  reportAuthChallenge,
  reportFetchStatus,
} from "./authGate";

beforeEach(() => {
  localStorage.clear();
  clearToken();
  clearNeedsAuth();
});

describe("token store", () => {
  it("stores, reads and clears the per-browser credential", () => {
    expect(getToken()).toBeNull();
    setToken("gtk_abc_secret");
    expect(getToken()).toBe("gtk_abc_secret");
    expect(localStorage.getItem("gantry-bench-token")).toBe("gtk_abc_secret");
    clearToken();
    expect(getToken()).toBeNull();
    expect(localStorage.getItem("gantry-bench-token")).toBeNull();
  });

  it("trims and treats blank input as a clear", () => {
    setToken("  gtk_padded  ");
    expect(getToken()).toBe("gtk_padded");
    setToken("   ");
    expect(getToken()).toBeNull();
  });

  it("authHeaders carries the bearer only when a token is set", () => {
    expect(authHeaders()).toEqual({});
    setToken("gtk_x");
    expect(authHeaders()).toEqual({ Authorization: "Bearer gtk_x" });
  });

  it("withToken appends ?token / &token only when a token is set", () => {
    expect(withToken("http://h/export/e.csv")).toBe("http://h/export/e.csv");
    setToken("gtk y"); // exercises encoding
    expect(withToken("http://h/export/e.csv")).toBe("http://h/export/e.csv?token=gtk%20y");
    expect(withToken("http://h/export/e.csv?format=wide")).toBe(
      "http://h/export/e.csv?format=wide&token=gtk%20y",
    );
  });
});

describe("auth gate", () => {
  it("classifies a ConnectError Unauthenticated as a challenge; PermissionDenied is not", () => {
    expect(isAuthChallenge(new ConnectError("x", Code.Unauthenticated))).toBe(true);
    expect(isAuthChallenge(new ConnectError("x", Code.PermissionDenied))).toBe(false);
    expect(isAuthChallenge(new Error("nope"))).toBe(false);
  });

  it("reportAuthChallenge flips the gate on a 401 ConnectError only", () => {
    expect(getNeedsAuth()).toBe(false);
    reportAuthChallenge(new ConnectError("scope", Code.PermissionDenied));
    expect(getNeedsAuth()).toBe(false); // 403 must not re-prompt
    reportAuthChallenge(new ConnectError("auth", Code.Unauthenticated));
    expect(getNeedsAuth()).toBe(true);
  });

  it("reportFetchStatus flips the gate on 401 and ignores other statuses", () => {
    reportFetchStatus(403);
    expect(getNeedsAuth()).toBe(false);
    reportFetchStatus(401);
    expect(getNeedsAuth()).toBe(true);
    clearNeedsAuth();
    expect(getNeedsAuth()).toBe(false);
  });
});
