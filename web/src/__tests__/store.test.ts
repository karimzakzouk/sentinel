import { describe, it, expect } from "vitest";
import { normalizeSSEEvent } from "@/lib/store";

describe("normalizeSSEEvent", () => {
  it("skips the feed for 'connected' events (initial ack)", () => {
    const r = normalizeSSEEvent({ type: "connected", data: {} });
    expect(r.skipFeed).toBe(true);
    expect(r.message).toBe("");
  });

  it("maps incident.started → AlertTriangle + service: started", () => {
    const r = normalizeSSEEvent({
      type: "incident",
      data: { service: "checkout-api", type: "started" },
    });
    expect(r.skipFeed).toBe(false);
    expect(r.icon).toBe("AlertTriangle");
    expect(r.message).toBe("checkout-api: started");
  });

  it("maps incident.* (non-started) → CheckCircle2", () => {
    const r = normalizeSSEEvent({
      type: "incident",
      data: { service: "checkout-api", type: "resolved" },
    });
    expect(r.icon).toBe("CheckCircle2");
    expect(r.message).toBe("checkout-api: resolved");
  });

  it("maps incident with missing type → 'updated'", () => {
    const r = normalizeSSEEvent({
      type: "incident",
      data: { service: "x" },
    });
    expect(r.message).toBe("x: updated");
  });

  it("maps pipeline events with phase + status", () => {
    const r = normalizeSSEEvent({
      type: "pipeline",
      data: { service: "x", phase: "investigation", status: "complete" },
    });
    expect(r.icon).toBe("Radio");
    expect(r.message).toBe("x — investigation: complete");
  });

  it("trims trailing dashes from pipeline message", () => {
    const r = normalizeSSEEvent({
      type: "pipeline",
      data: { service: "x", phase: "investigation", status: "" },
    });
    expect(r.message).toBe("x — investigation");
  });

  it("falls back to a non-empty message when phase + status are empty", () => {
    const r = normalizeSSEEvent({
      type: "pipeline",
      data: { service: "x" },
    });
    expect(r.skipFeed).toBe(false);
    expect(r.message.length).toBeGreaterThan(0);
  });

  it("falls back to Activity icon + message for unknown event types", () => {
    const r = normalizeSSEEvent({
      type: "weird-event",
      data: { message: "something happened" },
    });
    expect(r.icon).toBe("Activity");
    expect(r.message).toBe("something happened");
  });

  it("includes JSON snippet when no message is present for unknown events", () => {
    const r = normalizeSSEEvent({
      type: "weird-event",
      data: { foo: "bar" },
    });
    expect(r.icon).toBe("Activity");
    expect(r.message).toContain("weird-event");
    expect(r.message).toContain("foo");
  });

  it("handles missing data object", () => {
    const r = normalizeSSEEvent({ type: "unknown", data: {} });
    expect(r.skipFeed).toBe(false);
    expect(r.icon).toBe("Activity");
  });

  it("handles unknown type with no data at all", () => {
    const r = normalizeSSEEvent({ type: "", data: {} });
    expect(r.icon).toBe("Activity");
    expect(r.skipFeed).toBe(false);
  });
});
