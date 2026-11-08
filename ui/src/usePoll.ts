import { useEffect, useState } from "react";

export interface Polled<T> {
  data?: T;
  error?: string;
}

// usePoll loads now and every ten seconds while the page is visible. A
// failed refresh keeps the last data and reports the error.
export function usePoll<T>(load: () => Promise<T>, deps: unknown[], every = 10_000): Polled<T> {
  const [state, setState] = useState<Polled<T>>({});
  useEffect(() => {
    let alive = true;
    const run = () =>
      load().then(
        (data) => alive && setState({ data }),
        (e: unknown) => alive && setState((s) => ({ ...s, error: e instanceof Error ? e.message : String(e) })),
      );
    void run();
    const timer = setInterval(() => {
      if (!document.hidden) void run();
    }, every);
    return () => {
      alive = false;
      clearInterval(timer);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps -- callers list what the load depends on.
  }, deps);
  return state;
}
