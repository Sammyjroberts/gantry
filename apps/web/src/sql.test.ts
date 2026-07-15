import { describe, it, expect, beforeEach } from "vitest";
import {
  DISPLAY_ROW_CAP,
  HISTORY_LIMIT,
  SQL_SNIPPETS,
  formatCell,
  loadHistory,
  pushHistory,
  saveHistory,
  toGrid,
  type SqlResponse,
} from "./sql";

describe("formatCell", () => {
  it("renders null/undefined as the empty glyph", () => {
    expect(formatCell(null)).toBe("∅");
    expect(formatCell(undefined)).toBe("∅");
  });
  it("passes strings through and stringifies numbers/bools", () => {
    expect(formatCell("hi")).toBe("hi");
    expect(formatCell(42)).toBe("42");
    expect(formatCell(true)).toBe("true");
    expect(formatCell(false)).toBe("false");
  });
  it("renders non-finite numbers as empty", () => {
    expect(formatCell(NaN)).toBe("∅");
    expect(formatCell(Infinity)).toBe("∅");
  });
  it("stringifies bigints and JSON-encodes objects", () => {
    expect(formatCell(123n)).toBe("123");
    expect(formatCell({ a: 1 })).toBe('{"a":1}');
  });
});

describe("toGrid", () => {
  it("aligns row cells to the column order", () => {
    const res: SqlResponse = {
      columns: ["channel", "n"],
      rows: [
        { n: 3, channel: "pitch" },
        { n: 5, channel: "roll" },
      ],
      row_count: 2,
      truncated: false,
    };
    const g = toGrid(res);
    expect(g.columns).toEqual(["channel", "n"]);
    expect(g.rows).toEqual([
      ["pitch", "3"],
      ["roll", "5"],
    ]);
    expect(g.rowCount).toBe(2);
    expect(g.hiddenByDisplayCap).toBe(0);
    expect(g.serverTruncated).toBe(false);
  });

  it("fills missing columns with the empty glyph", () => {
    const res: SqlResponse = {
      columns: ["a", "b"],
      rows: [{ a: 1 }],
      row_count: 1,
      truncated: false,
    };
    expect(toGrid(res).rows).toEqual([["1", "∅"]]);
  });

  it("caps rendered rows at DISPLAY_ROW_CAP but preserves row_count", () => {
    const rows = Array.from({ length: DISPLAY_ROW_CAP + 20 }, (_, i) => ({ i }));
    const res: SqlResponse = { columns: ["i"], rows, row_count: rows.length, truncated: false };
    const g = toGrid(res);
    expect(g.rows.length).toBe(DISPLAY_ROW_CAP);
    expect(g.hiddenByDisplayCap).toBe(20);
    expect(g.rowCount).toBe(DISPLAY_ROW_CAP + 20);
  });

  it("surfaces the server truncation flag", () => {
    const res: SqlResponse = { columns: ["x"], rows: [{ x: 1 }], row_count: 999, truncated: true };
    expect(toGrid(res).serverTruncated).toBe(true);
  });

  it("tolerates a columnless / empty result", () => {
    const g = toGrid({ columns: [], rows: [], row_count: 0, truncated: false });
    expect(g.columns).toEqual([]);
    expect(g.rows).toEqual([]);
    expect(g.rowCount).toBe(0);
  });
});

describe("pushHistory", () => {
  it("prepends the newest query", () => {
    expect(pushHistory(["a"], "b")).toEqual(["b", "a"]);
  });
  it("trims and ignores blank queries", () => {
    expect(pushHistory(["a"], "   ")).toEqual(["a"]);
    expect(pushHistory([], "  SELECT 1  ")).toEqual(["SELECT 1"]);
  });
  it("de-duplicates by moving an existing query to the front", () => {
    expect(pushHistory(["a", "b", "c"], "c")).toEqual(["c", "a", "b"]);
  });
  it("caps at HISTORY_LIMIT", () => {
    const many = Array.from({ length: HISTORY_LIMIT }, (_, i) => `q${i}`);
    const next = pushHistory(many, "fresh");
    expect(next.length).toBe(HISTORY_LIMIT);
    expect(next[0]).toBe("fresh");
    expect(next).not.toContain(`q${HISTORY_LIMIT - 1}`); // oldest evicted
  });
});

describe("history persistence (localStorage)", () => {
  beforeEach(() => globalThis.localStorage?.clear());

  it("round-trips through save/load", () => {
    saveHistory(["one", "two"]);
    expect(loadHistory()).toEqual(["one", "two"]);
  });

  it("returns [] when nothing is stored", () => {
    expect(loadHistory()).toEqual([]);
  });

  it("tolerates corrupt storage", () => {
    globalThis.localStorage.setItem("gantry-sql-history", "{not json");
    expect(loadHistory()).toEqual([]);
  });

  it("ignores non-string entries and caps on load", () => {
    const arr = [...Array.from({ length: HISTORY_LIMIT + 5 }, (_, i) => `q${i}`), 123, null];
    globalThis.localStorage.setItem("gantry-sql-history", JSON.stringify(arr));
    const loaded = loadHistory();
    expect(loaded.length).toBe(HISTORY_LIMIT);
    expect(loaded.every((x) => typeof x === "string")).toBe(true);
  });
});

describe("SQL_SNIPPETS", () => {
  it("ships runnable starter snippets over tlm + catalog", () => {
    expect(SQL_SNIPPETS.length).toBeGreaterThanOrEqual(3);
    for (const s of SQL_SNIPPETS) {
      expect(s.label).toBeTruthy();
      expect(s.sql.toUpperCase()).toContain("SELECT");
    }
    // latency snippet uses the received_ns - ts_ns pattern.
    expect(SQL_SNIPPETS.some((s) => /received_ns\s*-\s*ts_ns/.test(s.sql))).toBe(true);
    // experiment-join snippet references the attached catalog.
    expect(SQL_SNIPPETS.some((s) => /catalog\./.test(s.sql))).toBe(true);
  });
});
