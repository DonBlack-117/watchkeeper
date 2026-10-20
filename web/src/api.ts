import type {
  Check,
  CheckInput,
  CheckPatch,
  FieldError,
  HourLatency,
  Incident,
  Me,
  PublicStatus,
  Summary,
} from "./types";

/** An RFC 9457 problem returned by the API, or a network failure (status 0). */
export class ApiError extends Error {
  readonly status: number;
  readonly fields: FieldError[];

  constructor(status: number, message: string, fields: FieldError[] = []) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.fields = fields;
  }
}

interface Problem {
  title?: string;
  detail?: string;
  errors?: FieldError[];
}

let onUnauthorized: (() => void) | undefined;

/** Called when any request answers 401, so the app can show the login form. */
export function setUnauthorizedHandler(fn: (() => void) | undefined): void {
  onUnauthorized = fn;
}

async function request<T>(
  method: string,
  path: string,
  body?: unknown,
  signal?: AbortSignal,
): Promise<T> {
  let res: Response;
  try {
    res = await fetch(path, {
      method,
      credentials: "same-origin",
      ...(signal ? { signal } : {}),
      headers:
        body === undefined
          ? { Accept: "application/json" }
          : { Accept: "application/json", "Content-Type": "application/json" },
      ...(body === undefined ? {} : { body: JSON.stringify(body) }),
    });
  } catch (e) {
    if (
      signal?.aborted &&
      e instanceof DOMException &&
      e.name === "AbortError"
    ) {
      throw e; // cancelled on purpose; callers ignore it
    }
    throw new ApiError(
      0,
      signal?.aborted
        ? "The server took too long to answer."
        : "Cannot reach the server. Check your connection and try again.",
    );
  }
  if (res.status === 204) {
    return undefined as T;
  }
  const text = await res.text();
  if (!res.ok) {
    let p: Problem = {};
    try {
      p = JSON.parse(text) as Problem;
    } catch {
      // not a problem document; keep the generic message below
    }
    if (res.status === 401 && path !== "/api/v1/auth/login") {
      onUnauthorized?.();
    }
    throw new ApiError(
      res.status,
      p.detail ?? p.title ?? `Request failed (${res.status})`,
      p.errors ?? [],
    );
  }
  // A proxy or captive portal can answer 200 with HTML; report it plainly.
  let parsed: unknown;
  try {
    parsed = JSON.parse(text);
  } catch {
    throw new ApiError(res.status, "Unexpected response from the server.");
  }
  if (typeof parsed !== "object" || parsed === null || !("data" in parsed)) {
    throw new ApiError(res.status, "Unexpected response from the server.");
  }
  return (parsed as { data: T }).data;
}

export const api = {
  me: () => request<Me>("GET", "/api/v1/auth/me"),
  login: (username: string, password: string) =>
    request<{ username: string }>("POST", "/api/v1/auth/login", {
      username,
      password,
    }),
  logout: () => request<undefined>("POST", "/api/v1/auth/logout"),

  checks: (signal?: AbortSignal) =>
    request<Check[]>("GET", "/api/v1/checks", undefined, signal),
  summary: (signal?: AbortSignal) =>
    request<Summary>("GET", "/api/v1/summary", undefined, signal),
  latency: (id: number, signal?: AbortSignal, window = "24h") =>
    request<HourLatency[]>(
      "GET",
      `/api/v1/checks/${id}/latency?window=${encodeURIComponent(window)}`,
      undefined,
      signal,
    ),
  createCheck: (input: CheckInput) =>
    request<Check>("POST", "/api/v1/checks", input),
  updateCheck: (id: number, input: CheckPatch) =>
    request<Check>("PATCH", `/api/v1/checks/${id}`, input),
  deleteCheck: (id: number) =>
    request<undefined>("DELETE", `/api/v1/checks/${id}`),

  incidents: (signal?: AbortSignal) =>
    request<Incident[]>("GET", "/api/v1/incidents?limit=10", undefined, signal),
  testAlert: () => request<undefined>("POST", "/api/v1/alerts/test"),

  status: (signal?: AbortSignal) =>
    request<PublicStatus>("GET", "/api/v1/status", undefined, signal),
};
