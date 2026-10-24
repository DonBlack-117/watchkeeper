import type { Page, Route } from "@playwright/test";

const days = Array.from({ length: 90 }, (_, i) =>
  i < 60 ? null : i === 75 ? 96.5 : 100,
);
const now = Date.now();
const hours = Array.from({ length: 24 }, (_, i) => {
  const hour = new Date(
    Math.floor(now / 3_600_000) * 3_600_000 - (23 - i) * 3_600_000,
  ).toISOString();
  return {
    hour,
    total: 60,
    ok: 60,
    p50: 120 + (i % 5) * 8,
    p95: 260 + (i % 7) * 30,
  };
});

export const checks = [
  {
    id: 1,
    name: "Bitso API",
    kind: "http",
    target: "https://api.bitso.com/v3/ticker/?book=btc_mxn",
    intervalS: 60,
    timeoutMs: 10000,
    enabled: true,
    createdAt: "2026-10-19T00:00:00Z",
    updatedAt: "2026-10-19T00:00:00Z",
    status: "operational",
    uptime24h: 100,
    p95Ms24h: 310,
    uptime90: 99.92,
    daily: days,
    lastResult: {
      id: 9,
      checkId: 1,
      startedAt: new Date(now - 40_000).toISOString(),
      ok: true,
      totalMs: 140,
    },
  },
  {
    id: 2,
    name: "donblack.dev TLS",
    kind: "tls",
    target: "donblack.dev:443",
    intervalS: 3600,
    timeoutMs: 10000,
    enabled: true,
    createdAt: "2026-10-19T00:00:00Z",
    updatedAt: "2026-10-19T00:00:00Z",
    status: "down",
    uptime90: 91.3,
    daily: days,
    lastResult: {
      id: 10,
      checkId: 2,
      startedAt: new Date(now - 120_000).toISOString(),
      ok: false,
      totalMs: 10000,
      error: "timeout",
    },
  },
];

const hoursAgo = (h: number) => new Date(now - h * 3_600_000).toISOString();

export const incidents = [
  {
    id: 2,
    checkId: 2,
    checkName: "donblack.dev TLS",
    status: "down",
    startedAt: hoursAgo(2),
    resolvedAt: null,
    updates: [
      { at: hoursAgo(2), status: "degraded", error: "timeout" },
      { at: hoursAgo(1.9), status: "down", error: "timeout" },
    ],
    triage: {
      id: 4,
      incidentId: 2,
      state: "done",
      trigger: "auto",
      model: "claude-opus-5-5",
      summary:
        "donblack.dev stopped answering on port 443; DNS still resolves.",
      probableCause:
        "The server accepts no TCP connections on 443, so the web server or its firewall is down. DNS answers with the same address as before.",
      confidence: "high",
      evidence: [
        "probe_now: timeout after 10000 ms",
        "dns_lookup A: 76.76.21.21, unchanged",
      ],
      nextSteps: [
        "Check that the web server is running on the host",
        "Review recent firewall changes",
      ],
      toolCalls: [
        {
          name: "recent_results",
          input: '{"limit":10}',
          outcome: "10 runs, 10 failed",
        },
        {
          name: "dns_lookup",
          input: '{"record_type":"A"}',
          outcome: "A: 1 records",
        },
        { name: "probe_now", outcome: "failed: timeout in 10000 ms" },
      ],
      inputTokens: 18200,
      outputTokens: 950,
      startedAt: hoursAgo(1.89),
      finishedAt: hoursAgo(1.88),
    } as Record<string, unknown> | undefined,
  },
  {
    id: 1,
    checkId: 1,
    checkName: "Bitso API",
    status: "degraded",
    startedAt: hoursAgo(50),
    resolvedAt: hoursAgo(49.6),
    updates: [
      { at: hoursAgo(50), status: "degraded", error: "HTTP 503" },
      { at: hoursAgo(49.6), status: "resolved" },
    ],
    triage: undefined as Record<string, unknown> | undefined,
  },
];

const json = (route: Route, status: number, data?: unknown) =>
  route.fulfill({
    status,
    contentType:
      status >= 400 ? "application/problem+json" : "application/json",
    body: data === undefined ? "" : JSON.stringify(data),
  });

/** Mocks the API for a signed-out user who signs in with diego / correct horse battery. */
export async function mockApi(page: Page, opts: { signedIn?: boolean } = {}) {
  let signedIn = opts.signedIn ?? false;
  const created: unknown[] = [];
  // Incidents investigated through the mock, answered as finished right away.
  const investigated = new Set<number>();

  await page.route("**/api/v1/**", async (route) => {
    const req = route.request();
    const url = new URL(req.url());
    const key = `${req.method()} ${url.pathname}`;
    const triageMatch = /^POST \/api\/v1\/incidents\/(\d+)\/triage$/.exec(key);
    if (triageMatch) {
      investigated.add(Number(triageMatch[1]));
      return json(route, 202, {
        data: {
          triageId: 9,
          incidentId: Number(triageMatch[1]),
          state: "running",
        },
      });
    }
    switch (key) {
      case "GET /api/v1/auth/me":
        return json(route, 200, {
          data: {
            authRequired: true,
            authenticated: signedIn,
            ...(signedIn ? { username: "diego" } : {}),
          },
        });
      case "POST /api/v1/auth/login": {
        const body = req.postDataJSON() as {
          username: string;
          password: string;
        };
        if (
          body.username === "diego" &&
          body.password === "correct horse battery"
        ) {
          signedIn = true;
          return json(route, 200, { data: { username: "diego" } });
        }
        return json(route, 401, {
          title: "Unauthorized",
          status: 401,
          detail: "wrong username or password",
        });
      }
      case "POST /api/v1/auth/logout":
        signedIn = false;
        return route.fulfill({ status: 204 });
      case "GET /api/v1/checks":
        return json(route, 200, { data: [...checks, ...created] });
      case "POST /api/v1/checks": {
        const body = req.postDataJSON() as { name: string };
        const c = {
          ...checks[0],
          ...body,
          id: 3,
          status: "unknown",
          daily: undefined,
          uptime90: undefined,
          lastResult: undefined,
        };
        created.push(c);
        return json(route, 201, { data: c });
      }
      case "GET /api/v1/incidents":
        return json(route, 200, {
          data: incidents.map((i) =>
            investigated.has(i.id)
              ? {
                  ...i,
                  triage: {
                    id: 9,
                    incidentId: i.id,
                    state: "done",
                    trigger: "manual",
                    model: "claude-opus-5-5",
                    summary:
                      "Bitso answered 503 for 20 minutes during maintenance.",
                    confidence: "medium",
                    evidence: [],
                    nextSteps: [],
                    toolCalls: [],
                    inputTokens: 9000,
                    outputTokens: 400,
                    startedAt: new Date().toISOString(),
                    finishedAt: new Date().toISOString(),
                  },
                }
              : i,
          ),
        });
      case "POST /api/v1/alerts/test":
        return route.fulfill({ status: 204 });
      case "GET /api/v1/summary":
        return json(route, 200, {
          data: {
            triage: { enabled: true, model: "claude-opus-5-5" },
            openIncidents: 1,
            alertChannels: ["telegram"],
            checks: 2 + created.length,
            enabled: 2,
            operational: 1,
            degraded: 0,
            down: 1,
            unknown: 0,
            uptime24h: 99.31,
            p95Ms24h: 342,
            runs24h: 1464,
          },
        });
      case "GET /api/v1/status":
        return json(route, 200, {
          data: {
            overall: "down",
            updatedAt: new Date().toISOString(),
            checks: checks.map((c) => ({
              id: c.id,
              name: c.name,
              kind: c.kind,
              status: c.status,
              uptime90: c.uptime90,
              lastCheckedAt: null,
              daily: c.daily,
            })),
            incidents: incidents.map((inc) => ({
              id: inc.id,
              checkId: inc.checkId,
              name: inc.checkName,
              status: inc.status,
              startedAt: inc.startedAt,
              resolvedAt: inc.resolvedAt,
              updates: inc.updates.map(({ at, status }) => ({ at, status })),
            })),
          },
        });
    }
    if (/^GET \/api\/v1\/checks\/\d+\/latency$/.test(key))
      return json(route, 200, { data: hours });
    return json(route, 404, { title: "Not found", status: 404 });
  });
}
