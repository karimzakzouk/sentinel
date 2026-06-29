"use client";

// SSE subscription hook — single shared EventSource across the app.
// Mirrors the singleton-store pattern used by use-toast so every component
// that calls useSSE() shares one connection and one event buffer.

import { useEffect, useState } from "react";

export interface SSEEventItem {
  id: number;
  type: string;
  data: Record<string, unknown>;
  timestamp: number;
}

export type SSEStatus = "connecting" | "open" | "error";

const MAX_EVENTS = 100;
let eventSource: EventSource | null = null;
let nextId = 0;
const buffer: SSEEventItem[] = [];
const listeners = new Set<(e: SSEEventItem[]) => void>();
const statusListeners = new Set<(s: SSEStatus) => void>();
let status: SSEStatus = "connecting";

function emit() {
  const snapshot = buffer.slice();
  listeners.forEach((l) => l(snapshot));
}

function setStatus(s: SSEStatus) {
  status = s;
  statusListeners.forEach((l) => l(s));
}

function ensureConnected() {
  if (typeof window === "undefined") return;
  if (eventSource) return;
  try {
    setStatus("connecting");
    const es = new EventSource("/events");
    eventSource = es;
    es.onopen = () => setStatus("open");
    es.onmessage = (msg) => {
      try {
        const parsed = JSON.parse(msg.data);
        const item: SSEEventItem = {
          id: ++nextId,
          type: parsed.type || "message",
          data: parsed.data || parsed,
          timestamp: Date.now(),
        };
        buffer.unshift(item);
        if (buffer.length > MAX_EVENTS) buffer.length = MAX_EVENTS;
        emit();
      } catch {
        // ignore non-JSON keepalives
      }
    };
    es.onerror = () => {
      setStatus("error");
      // EventSource auto-reconnects; we leave it alone.
    };
  } catch {
    setStatus("error");
  }
}

export function useSSE() {
  const [events, setEvents] = useState<SSEEventItem[]>(buffer);
  const [currentStatus, setCurrentStatus] = useState<SSEStatus>(status);

  useEffect(() => {
    ensureConnected();
    const l = (e: SSEEventItem[]) => setEvents(e);
    const sl = (s: SSEStatus) => setCurrentStatus(s);
    listeners.add(l);
    statusListeners.add(sl);
    setEvents(buffer.slice());
    setCurrentStatus(status);
    return () => {
      listeners.delete(l);
      statusListeners.delete(sl);
    };
  }, []);

  return { events, status: currentStatus };
}

/** Get the count of events seen so far — useful as a useEffect dependency to trigger refetches. */
export function useSSECount(): number {
  const { events } = useSSE();
  return events.length;
}
