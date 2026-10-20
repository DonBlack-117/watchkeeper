import { act, renderHook } from "@testing-library/react";
import { usePoll } from "./usePoll";

beforeEach(() => {
  vi.useFakeTimers();
});
afterEach(() => {
  vi.useRealTimers();
});

/** A fetcher whose calls resolve only when the test says so. */
function controlled() {
  const pending: { key: string; resolve: (v: string) => void }[] = [];
  const fetcher = vi.fn(
    (key: string) =>
      new Promise<string>((resolve) => pending.push({ key, resolve })),
  );
  return { fetcher, pending };
}

describe("usePoll", () => {
  it("polls on the interval and keeps one request in flight", async () => {
    const { fetcher, pending } = controlled();
    renderHook(() => usePoll(() => fetcher("a"), 1000));
    expect(fetcher).toHaveBeenCalledTimes(1);

    // Becoming visible while a request is in flight must not start a second one.
    act(() => {
      document.dispatchEvent(new Event("visibilitychange"));
    });
    expect(fetcher).toHaveBeenCalledTimes(1);

    await act(async () => {
      pending[0]?.resolve("one");
      await Promise.resolve();
    });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1000);
    });
    expect(fetcher).toHaveBeenCalledTimes(2);
  });

  it("never shows the previous key's data", async () => {
    const { fetcher, pending } = controlled();
    const { result, rerender } = renderHook(
      ({ k }) => usePoll(() => fetcher(k), 1000, k),
      {
        initialProps: { k: "a" },
      },
    );
    await act(async () => {
      pending[0]?.resolve("data-a");
      await Promise.resolve();
    });
    expect(result.current.data).toBe("data-a");

    rerender({ k: "b" });
    expect(result.current.data).toBeUndefined();
    expect(result.current.loading).toBe(true);

    await act(async () => {
      pending[1]?.resolve("data-b");
      await Promise.resolve();
    });
    expect(result.current.data).toBe("data-b");
  });

  it("aborts the request and stops polling on unmount", async () => {
    let signal: AbortSignal | undefined;
    const fetcher = vi.fn((s: AbortSignal) => {
      signal = s;
      return new Promise<string>(() => undefined);
    });
    const { unmount } = renderHook(() => usePoll(fetcher, 1000));
    unmount();
    expect(signal?.aborted).toBe(true);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5000);
    });
    expect(fetcher).toHaveBeenCalledTimes(1);
  });
});
