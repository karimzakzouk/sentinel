import { describe, it, expect } from "vitest";
import {
  cn,
  timeAgo,
  timeMDHM,
  formatDuration,
  formatCost,
  formatTokens,
  formatConfidence,
  truncate,
  shortId,
  parseCsv,
  joinCsv,
  isRecord,
  asString,
} from "@/lib/utils";

describe("cn", () => {
  it("merges class names", () => {
    expect(cn("a", "b")).toBe("a b");
  });

  it("filters falsy values", () => {
    expect(cn("a", false, null, undefined, "b")).toBe("a b");
  });

  it("dedupes conflicting tailwind classes (last wins)", () => {
    expect(cn("p-2", "p-4")).toBe("p-4");
  });
});

describe("timeAgo", () => {
  it("returns — for null/undefined", () => {
    expect(timeAgo(null)).toBe("—");
    expect(timeAgo(undefined)).toBe("—");
  });

  it("returns — for invalid dates", () => {
    expect(timeAgo("not-a-date")).toBe("—");
  });

  it("returns 'just now' for < 5s ago", () => {
    const now = new Date(Date.now() - 1000).toISOString();
    expect(timeAgo(now)).toBe("just now");
  });

  it("returns '5s ago' for 5 seconds ago", () => {
    const now = new Date(Date.now() - 5000).toISOString();
    expect(timeAgo(now)).toBe("5s ago");
  });

  it("returns '5m ago' for 5 minutes ago", () => {
    const now = new Date(Date.now() - 5 * 60_000).toISOString();
    expect(timeAgo(now)).toBe("5m ago");
  });

  it("returns '5h ago' for 5 hours ago", () => {
    const now = new Date(Date.now() - 5 * 3_600_000).toISOString();
    expect(timeAgo(now)).toBe("5h ago");
  });

  it("returns '2d ago' for 2 days ago", () => {
    const now = new Date(Date.now() - 2 * 86_400_000).toISOString();
    expect(timeAgo(now)).toBe("2d ago");
  });

  it("accepts Date objects", () => {
    const d = new Date(Date.now() - 10_000);
    expect(timeAgo(d)).toBe("10s ago");
  });
});

describe("timeMDHM", () => {
  it("returns — for null", () => {
    expect(timeMDHM(null)).toBe("—");
  });

  it("formats as MM-DD HH:MM", () => {
    const d = new Date(2026, 0, 5, 14, 7);
    expect(timeMDHM(d)).toBe("01-05 14:07");
  });
});

describe("formatDuration", () => {
  it("returns — for null/undefined/NaN", () => {
    expect(formatDuration(null)).toBe("—");
    expect(formatDuration(undefined)).toBe("—");
    expect(formatDuration(NaN)).toBe("—");
  });

  it("formats seconds", () => {
    expect(formatDuration(45)).toBe("45s");
  });

  it("formats minutes + seconds", () => {
    expect(formatDuration(83)).toBe("1m 23s");
    expect(formatDuration(60)).toBe("1m");
  });

  it("formats hours + minutes", () => {
    expect(formatDuration(3900)).toBe("1h 5m");
    expect(formatDuration(3600)).toBe("1h");
  });
});

describe("formatCost", () => {
  it("returns $0.00 for null/NaN", () => {
    expect(formatCost(null)).toBe("$0.00");
    expect(formatCost(NaN)).toBe("$0.00");
  });

  it("returns 4 decimal places for sub-cent", () => {
    expect(formatCost(0.001)).toBe("$0.0010");
  });

  it("returns 3 decimal places for sub-dollar", () => {
    expect(formatCost(0.5)).toBe("$0.500");
  });

  it("returns 2 decimal places for dollar+", () => {
    expect(formatCost(1.5)).toBe("$1.50");
  });
});

describe("formatTokens", () => {
  it("returns — for null/NaN", () => {
    expect(formatTokens(null)).toBe("—");
    expect(formatTokens(NaN)).toBe("—");
  });

  it("formats plain numbers under 1k", () => {
    expect(formatTokens(500)).toBe("500");
  });

  it("formats k", () => {
    expect(formatTokens(1500)).toBe("1.5k");
  });

  it("formats M", () => {
    expect(formatTokens(1_500_000)).toBe("1.50M");
  });
});

describe("formatConfidence", () => {
  it("returns — for null/NaN", () => {
    expect(formatConfidence(null)).toBe("—");
    expect(formatConfidence(NaN)).toBe("—");
  });

  it("formats 0-1 as percentage", () => {
    expect(formatConfidence(0.85)).toBe("85%");
  });

  it("formats already-percent values", () => {
    expect(formatConfidence(85)).toBe("85%");
  });
});

describe("truncate", () => {
  it("returns empty for null/undefined", () => {
    expect(truncate(null, 5)).toBe("");
    expect(truncate(undefined, 5)).toBe("");
  });

  it("returns short strings unchanged", () => {
    expect(truncate("abc", 10)).toBe("abc");
  });

  it("truncates long strings with ellipsis", () => {
    expect(truncate("abcdefghij", 5)).toBe("abcd…");
  });
});

describe("shortId", () => {
  it("returns — for null/undefined/empty", () => {
    expect(shortId(null)).toBe("—");
    expect(shortId(undefined)).toBe("—");
    expect(shortId("")).toBe("—");
  });

  it("returns short IDs unchanged", () => {
    expect(shortId("abc123")).toBe("abc123");
  });

  it("truncates long IDs to 8 chars", () => {
    expect(shortId("0123456789abcdef")).toBe("01234567");
  });
});

describe("parseCsv / joinCsv", () => {
  it("parseCsv returns empty for null/empty", () => {
    expect(parseCsv(null)).toEqual([]);
    expect(parseCsv("")).toEqual([]);
  });

  it("parseCsv trims and filters empties", () => {
    expect(parseCsv("a, b , , c")).toEqual(["a", "b", "c"]);
  });

  it("joinCsv returns empty for null/empty array", () => {
    expect(joinCsv(null)).toBe("");
    expect(joinCsv([])).toBe("");
  });

  it("joinCsv joins with comma+space", () => {
    expect(joinCsv(["a", "b", "c"])).toBe("a, b, c");
  });
});

describe("isRecord", () => {
  it("returns true for plain objects", () => {
    expect(isRecord({})).toBe(true);
    expect(isRecord({ a: 1 })).toBe(true);
  });

  it("returns false for null", () => {
    expect(isRecord(null)).toBe(false);
  });

  it("returns false for arrays", () => {
    expect(isRecord([1, 2])).toBe(false);
  });

  it("returns false for primitives", () => {
    expect(isRecord("x")).toBe(false);
    expect(isRecord(42)).toBe(false);
    expect(isRecord(true)).toBe(false);
  });
});

describe("asString", () => {
  it("returns strings unchanged", () => {
    expect(asString("hello")).toBe("hello");
  });

  it("returns empty for null/undefined", () => {
    expect(asString(null)).toBe("");
    expect(asString(undefined)).toBe("");
  });

  it("stringifies other values", () => {
    expect(asString(42)).toBe("42");
    expect(asString(true)).toBe("true");
  });
});
