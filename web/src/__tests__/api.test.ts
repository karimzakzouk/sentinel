import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import {
  fetchJSON,
  listIncidents,
  getIncident,
  triggerInvestigation,
  pauseAutonomous,
  resumeAutonomous,
  listProviders,
  getAutoMergeState,
  getRuntimeConfig,
  updateRuntimeConfig,
  getMetricsSummary,
  getApiMeta,
  getHealth,
  validateToken,
  subscribeToEvents,
  getToken,
  setToken,
  clearToken,
} from "@/lib/api";

// Mock global fetch + localStorage + EventSource. We stub `window` so the
// api.ts SSR guard (`typeof window === "undefined"`) allows token reads.
const fetchMock = vi.fn();
const localStorageMock = (() => {
  let store: Record<string, string> = {};
  return {
    getItem: vi.fn((k: string) => store[k] ?? null),
    setItem: vi.fn((k: string, v: string) => {
      store[k] = v;
    }),
    removeItem: vi.fn((k: string) => {
      delete store[k];
    }),
    clear: vi.fn(() => {
      store = {};
    }),
  };
})();

const windowMock = {
  localStorage: localStorageMock,
  location: { reload: vi.fn() },
};

class MockEventSource {
  static instances: MockEventSource[] = [];
  url: string;
  onopen: ((ev: Event) => void) | null = null;
  onmessage: ((ev: MessageEvent) => void) | null = null;
  onerror: ((ev: Event) => void) | null = null;
  close = vi.fn(() => {
    this.closed = true;
  });
  closed = false;
  constructor(url: string) {
    this.url = url;
    MockEventSource.instances.push(this);
  }
}

describe("api client", () => {
  beforeEach(() => {
    vi.stubGlobal("fetch", fetchMock);
    vi.stubGlobal("localStorage", localStorageMock);
    vi.stubGlobal("window", windowMock);
    vi.stubGlobal("EventSource", MockEventSource);
    MockEventSource.instances.length = 0;
    localStorageMock.clear();
    fetchMock.mockReset();
    windowMock.location.reload.mockReset();
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  describe("token management", () => {
    it("getToken / setToken / clearToken read/write localStorage", () => {
      expect(getToken()).toBeNull();
      setToken("abc123");
      expect(getToken()).toBe("abc123");
      clearToken();
      expect(getToken()).toBeNull();
    });
  });

  describe("fetchJSON", () => {
    it("parses JSON on 200", async () => {
      fetchMock.mockResolvedValueOnce({
        ok: true,
        status: 200,
        json: async () => ({ hello: "world" }),
        text: async () => "{}",
      });
      const result = await fetchJSON<{ hello: string }>("/test");
      expect(result).toEqual({ hello: "world" });
    });

    it("returns undefined on 204", async () => {
      fetchMock.mockResolvedValueOnce({
        ok: true,
        status: 204,
        json: async () => ({}),
        text: async () => "",
      });
      const result = await fetchJSON("/test");
      expect(result).toBeUndefined();
    });

    it("throws on non-2xx", async () => {
      fetchMock.mockResolvedValueOnce({
        ok: false,
        status: 500,
        statusText: "Internal Server Error",
        text: async () => "boom",
      });
      await expect(fetchJSON("/test")).rejects.toThrow(/500.*boom/);
    });

    it("clears token + reloads on 401", async () => {
      setToken("abc");
      fetchMock.mockResolvedValueOnce({
        ok: false,
        status: 401,
        statusText: "Unauthorized",
        text: async () => "bad token",
      });
      await expect(fetchJSON("/test")).rejects.toThrow("Session expired");
      expect(getToken()).toBeNull();
      expect(windowMock.location.reload).toHaveBeenCalled();
    });

    it("injects Authorization header when token is set", async () => {
      setToken("xyz");
      fetchMock.mockResolvedValueOnce({
        ok: true,
        status: 200,
        json: async () => ({}),
        text: async () => "{}",
      });
      await fetchJSON("/test");
      const [, init] = fetchMock.mock.calls[0]!;
      expect(init.headers.Authorization).toBe("Bearer xyz");
    });
  });

  describe("getHealth", () => {
    it("returns parsed JSON on success", async () => {
      fetchMock.mockResolvedValueOnce({
        ok: true,
        status: 200,
        json: async () => ({ status: "ok", version: "1.0.0", auth_required: true }),
      });
      const h = await getHealth();
      expect(h).toEqual({ status: "ok", version: "1.0.0", auth_required: true });
    });

    it("returns {status: 'error'} on fetch failure", async () => {
      fetchMock.mockRejectedValueOnce(new Error("network down"));
      const h = await getHealth();
      expect(h).toEqual({ status: "error" });
    });

    it("returns {status: 'error'} on non-200", async () => {
      fetchMock.mockResolvedValueOnce({ ok: false, status: 500 });
      const h = await getHealth();
      expect(h).toEqual({ status: "error" });
    });
  });

  describe("listIncidents", () => {
    it("returns the array on success", async () => {
      fetchMock.mockResolvedValueOnce({
        ok: true,
        status: 200,
        json: async () => [{ incident_id: "abc" }, { incident_id: "def" }],
        text: async () => "[]",
      });
      const list = await listIncidents(10);
      expect(list).toHaveLength(2);
      expect(list[0]?.incident_id).toBe("abc");
    });

    it("returns empty array when response is null", async () => {
      fetchMock.mockResolvedValueOnce({
        ok: true,
        status: 200,
        json: async () => null,
        text: async () => "",
      });
      const list = await listIncidents();
      expect(list).toEqual([]);
    });
  });

  describe("triggerInvestigation", () => {
    it("POSTs to /admin/investigate with service+namespace+reason", async () => {
      fetchMock.mockResolvedValueOnce({
        ok: true,
        status: 200,
        json: async () => ({ status: "triggered", service: "checkout-api", cluster_id: "abc" }),
        text: async () => "{}",
      });
      const r = await triggerInvestigation("checkout-api", "default", "test");
      expect(r.status).toBe("triggered");
      const [, init] = fetchMock.mock.calls[0]!;
      expect(init.method).toBe("POST");
      expect(JSON.parse(init.body as string)).toEqual({
        service: "checkout-api",
        namespace: "default",
        reason: "test",
      });
    });

    it("defaults reason to 'manual trigger'", async () => {
      fetchMock.mockResolvedValueOnce({
        ok: true,
        status: 200,
        json: async () => ({ status: "triggered", service: "x", cluster_id: "y" }),
        text: async () => "{}",
      });
      await triggerInvestigation("x", "default");
      const [, init] = fetchMock.mock.calls[0]!;
      expect(JSON.parse(init.body as string).reason).toBe("manual trigger");
    });
  });

  describe("pauseAutonomous / resumeAutonomous", () => {
    it("POSTs to /admin/pause with duration_min + reason", async () => {
      fetchMock.mockResolvedValueOnce({
        ok: true,
        status: 200,
        json: async () => ({}),
        text: async () => "{}",
      });
      await pauseAutonomous(60, "deploy");
      const [, init] = fetchMock.mock.calls[0]!;
      expect(init.method).toBe("POST");
      expect(JSON.parse(init.body as string)).toEqual({ duration_min: 60, reason: "deploy" });
    });

    it("POSTs to /admin/resume", async () => {
      fetchMock.mockResolvedValueOnce({
        ok: true,
        status: 200,
        json: async () => ({}),
        text: async () => "{}",
      });
      await resumeAutonomous();
      const [url, init] = fetchMock.mock.calls[0]!;
      expect(url).toBe("/admin/resume");
      expect(init.method).toBe("POST");
    });
  });

  describe("listProviders", () => {
    it("returns the providers array when wrapped in {enabled, providers}", async () => {
      fetchMock.mockResolvedValueOnce({
        ok: true,
        status: 200,
        json: async () => ({ enabled: true, providers: [{ id: "p1" }, { id: "p2" }] }),
        text: async () => "{}",
      });
      const list = await listProviders();
      expect(list).toHaveLength(2);
    });

    it("returns the array directly when not wrapped", async () => {
      fetchMock.mockResolvedValueOnce({
        ok: true,
        status: 200,
        json: async () => [{ id: "p1" }],
        text: async () => "[]",
      });
      const list = await listProviders();
      expect(list).toHaveLength(1);
    });
  });

  describe("updateRuntimeConfig", () => {
    it("uses POST (not PUT — matches Go handleSettings)", async () => {
      fetchMock.mockResolvedValueOnce({
        ok: true,
        status: 200,
        json: async () => ({}),
        text: async () => "{}",
      });
      await updateRuntimeConfig({ llm_fast_model: "claude-haiku" });
      const [url, init] = fetchMock.mock.calls[0]!;
      expect(url).toBe("/settings");
      expect(init.method).toBe("POST");
      expect(JSON.parse(init.body as string)).toEqual({ llm_fast_model: "claude-haiku" });
    });
  });

  describe("getAutoMergeState / getRuntimeConfig / getMetricsSummary / getApiMeta / getIncident", () => {
    it("GETs /automerge", async () => {
      fetchMock.mockResolvedValueOnce({
        ok: true,
        status: 200,
        json: async () => ({ enabled: true }),
        text: async () => "{}",
      });
      const r = await getAutoMergeState();
      expect(r.enabled).toBe(true);
    });

    it("GETs /settings", async () => {
      fetchMock.mockResolvedValueOnce({
        ok: true,
        status: 200,
        json: async () => ({ llm_provider: "openrouter" }),
        text: async () => "{}",
      });
      const r = await getRuntimeConfig();
      expect(r.llm_provider).toBe("openrouter");
    });

    it("GETs /metrics-summary", async () => {
      fetchMock.mockResolvedValueOnce({
        ok: true,
        status: 200,
        json: async () => ({ active: 3, total: 10 }),
        text: async () => "{}",
      });
      const r = await getMetricsSummary();
      expect(r.active).toBe(3);
    });

    it("GETs /api/meta", async () => {
      fetchMock.mockResolvedValueOnce({
        ok: true,
        status: 200,
        json: async () => ({ version: "1.0.0", endpoints: ["/healthz"], commands: [], auth_required: false }),
        text: async () => "{}",
      });
      const r = await getApiMeta();
      expect(r.version).toBe("1.0.0");
      expect(r.endpoints).toEqual(["/healthz"]);
    });

    it("GETs /incidents/{id}", async () => {
      fetchMock.mockResolvedValueOnce({
        ok: true,
        status: 200,
        json: async () => ({ incident_id: "abc" }),
        text: async () => "{}",
      });
      const r = await getIncident("abc");
      expect(r.incident_id).toBe("abc");
    });
  });

  describe("validateToken", () => {
    it("returns true on 200", async () => {
      fetchMock.mockResolvedValueOnce({ ok: true, status: 200 });
      expect(await validateToken("good")).toBe(true);
    });

    it("returns false on non-200", async () => {
      fetchMock.mockResolvedValueOnce({ ok: false, status: 401 });
      expect(await validateToken("bad")).toBe(false);
    });

    it("returns false on network error", async () => {
      fetchMock.mockRejectedValueOnce(new Error("network"));
      expect(await validateToken("any")).toBe(false);
    });
  });

  describe("subscribeToEvents", () => {
    it("constructs EventSource with token query param when set", () => {
      setToken("mytoken");
      const es = subscribeToEvents({ onEvent: () => {} });
      expect(es.url).toBe("/events?token=mytoken");
    });

    it("constructs EventSource without query when no token", () => {
      clearToken();
      const es = subscribeToEvents({ onEvent: () => {} });
      expect(es.url).toBe("/events");
    });

    it("parses {type, data} payloads via onmessage", () => {
      const onEvent = vi.fn();
      const es = subscribeToEvents({ onEvent });
      const msg = { data: JSON.stringify({ type: "incident", data: { service: "x" } }) };
      es.onmessage?.(msg as MessageEvent);
      expect(onEvent).toHaveBeenCalledWith({ type: "incident", data: { service: "x" } });
    });

    it("normalizes flat payloads to {type: 'message', data: {}}", () => {
      const onEvent = vi.fn();
      const es = subscribeToEvents({ onEvent });
      const msg = { data: JSON.stringify("just a string") };
      es.onmessage?.(msg as MessageEvent);
      expect(onEvent).toHaveBeenCalledWith({ type: "message", data: {} });
    });

    it("ignores non-JSON keepalive pings", () => {
      const onEvent = vi.fn();
      const es = subscribeToEvents({ onEvent });
      const msg = { data: ": ping" };
      es.onmessage?.(msg as MessageEvent);
      expect(onEvent).not.toHaveBeenCalled();
    });
  });
});
