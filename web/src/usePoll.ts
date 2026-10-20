import { useCallback, useEffect, useRef, useState } from "react";

export interface Poll<T> {
  data: T | undefined;
  error: Error | undefined;
  loading: boolean;
  reload: () => void;
}

/** A request that hangs longer than this counts as failed, so polling never stalls. */
const REQUEST_TIMEOUT_MS = 15_000;

/**
 * Loads `fetcher` now and every `intervalMs` while the tab is visible. Only one
 * request is in flight at a time; a new `key` aborts it, clears the old data and
 * starts over, so the caller never shows one key's data under another.
 */
export function usePoll<T>(
  fetcher: (signal: AbortSignal) => Promise<T>,
  intervalMs: number,
  key: unknown = null,
): Poll<T> {
  const [state, setState] = useState<{
    key: unknown;
    data?: T;
    error?: Error;
    loading: boolean;
  }>({ key, loading: true });
  const fetcherRef = useRef(fetcher);
  const [tick, setTick] = useState(0);

  useEffect(() => {
    fetcherRef.current = fetcher;
  });

  useEffect(() => {
    let cancelled = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    let inFlight: AbortController | undefined;
    // A function, so the check is re-read after each await.
    const stopped = () => cancelled;

    const schedule = () => {
      clearTimeout(timer);
      timer = setTimeout(() => {
        if (document.visibilityState === "visible") void run();
        else schedule();
      }, intervalMs);
    };
    const run = async () => {
      if (cancelled || inFlight) return;
      clearTimeout(timer);
      const ctrl = new AbortController();
      inFlight = ctrl;
      try {
        const data = await fetcherRef.current(
          AbortSignal.any([
            ctrl.signal,
            AbortSignal.timeout(REQUEST_TIMEOUT_MS),
          ]),
        );
        if (!stopped()) setState({ key, data, loading: false });
      } catch (e) {
        if (!stopped())
          setState((s) => ({
            ...s,
            key,
            error: e instanceof Error ? e : new Error(String(e)),
            loading: false,
          }));
      } finally {
        inFlight = undefined;
      }
      if (!stopped()) schedule();
    };
    const onVisible = () => {
      if (document.visibilityState === "visible") void run();
    };

    // Same key: keep showing the current data while it refreshes.
    setState((s) =>
      Object.is(s.key, key) ? { ...s, loading: true } : { key, loading: true },
    );
    void run();
    document.addEventListener("visibilitychange", onVisible);
    return () => {
      cancelled = true;
      clearTimeout(timer);
      inFlight?.abort();
      document.removeEventListener("visibilitychange", onVisible);
    };
  }, [intervalMs, key, tick]);

  const reload = useCallback(() => {
    setTick((t) => t + 1);
  }, []);

  const current = Object.is(state.key, key);
  return {
    data: current ? state.data : undefined,
    error: current ? state.error : undefined,
    loading: current ? state.loading : true,
    reload,
  };
}
