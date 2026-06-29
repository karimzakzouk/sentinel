"use client";

import { useCallback, useEffect, useRef, useState } from "react";

interface FetchState<T> {
  data: T | null;
  loading: boolean;
  error: string | null;
}

/**
 * Generic data-fetching hook with manual refetch + auto-refresh interval.
 * `deps` changes trigger a refetch (like useEffect deps).
 */
export function useFetch<T>(
  fetcher: () => Promise<T>,
  deps: React.DependencyList = [],
  options: { intervalMs?: number; enabled?: boolean } = {},
) {
  const { intervalMs, enabled = true } = options;
  const [state, setState] = useState<FetchState<T>>({
    data: null,
    loading: true,
    error: null,
  });
  const fetcherRef = useRef(fetcher);
  fetcherRef.current = fetcher;
  const mountedRef = useRef(true);

  const refetch = useCallback(async () => {
    setState((s) => ({ ...s, loading: true, error: null }));
    try {
      const data = await fetcherRef.current();
      if (mountedRef.current) {
        setState({ data, loading: false, error: null });
      }
    } catch (err) {
      if (mountedRef.current) {
        setState({
          data: null,
          loading: false,
          error: err instanceof Error ? err.message : String(err),
        });
      }
    }
  }, []);

  useEffect(() => {
    mountedRef.current = true;
    if (!enabled) {
      setState({ data: null, loading: false, error: null });
      return;
    }
    refetch();
    return () => {
      mountedRef.current = false;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [enabled, ...deps]);

  useEffect(() => {
    if (!enabled || !intervalMs) return;
    const id = setInterval(() => {
      refetch();
    }, intervalMs);
    return () => clearInterval(id);
  }, [enabled, intervalMs, refetch]);

  return { ...state, refetch, setData: (data: T | null) => setState((s) => ({ ...s, data })) };
}
