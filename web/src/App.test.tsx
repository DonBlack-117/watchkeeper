import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { App } from "./App";

type Route = (init: RequestInit | undefined) => {
  status: number;
  body?: unknown;
};

/** Minimal fetch stub: one handler per "METHOD path" (query string ignored). */
function stubFetch(routes: Record<string, Route>) {
  const calls: { key: string; body: unknown }[] = [];
  vi.stubGlobal("fetch", (input: string, init?: RequestInit) => {
    const key = `${init?.method ?? "GET"} ${input.split("?")[0] ?? ""}`;
    calls.push({
      key,
      body: init?.body ? JSON.parse(init.body as string) : undefined,
    });
    const route = routes[key];
    const res = route
      ? route(init)
      : { status: 404, body: { title: "Not found" } };
    return Promise.resolve(
      new Response(res.body === undefined ? null : JSON.stringify(res.body), {
        status: res.status,
      }),
    );
  });
  return calls;
}

const days = Array<number | null>(90).fill(null);
days[89] = 100;

const check = {
  id: 1,
  name: "Bitso API",
  kind: "http",
  target: "https://api.bitso.com/v3/ticker",
  intervalS: 60,
  timeoutMs: 10000,
  enabled: true,
  createdAt: "2026-10-19T00:00:00Z",
  updatedAt: "2026-10-19T00:00:00Z",
  status: "operational",
  uptime90: 99.9,
  p95Ms24h: 180,
  daily: days,
};

const dashboardRoutes: Record<string, Route> = {
  "GET /api/v1/auth/me": () => ({
    status: 200,
    body: {
      data: { authRequired: true, authenticated: true, username: "diego" },
    },
  }),
  "GET /api/v1/checks": () => ({ status: 200, body: { data: [check] } }),
  "GET /api/v1/summary": () => ({
    status: 200,
    body: {
      data: {
        triage: { enabled: false },
        openIncidents: 1,
        alertChannels: ["telegram"],
        checks: 1,
        enabled: 1,
        operational: 1,
        degraded: 0,
        down: 0,
        unknown: 0,
        uptime24h: 99.5,
        p95Ms24h: 200,
        runs24h: 1440,
      },
    },
  }),
  "GET /api/v1/checks/1/latency": () => ({ status: 200, body: { data: [] } }),
  "GET /api/v1/incidents": () => ({
    status: 200,
    body: {
      data: [
        {
          id: 7,
          checkId: 1,
          checkName: "Bitso API",
          status: "down",
          startedAt: "2026-10-19T10:00:00Z",
          resolvedAt: null,
          updates: [
            { at: "2026-10-19T10:00:00Z", status: "down", error: "timeout" },
          ],
        },
      ],
    },
  }),
};

beforeEach(() => {
  window.history.replaceState(null, "", "/");
  // jsdom has no <dialog> modal support.
  HTMLDialogElement.prototype.showModal = function () {
    this.open = true;
  };
  HTMLDialogElement.prototype.close = function () {
    this.open = false;
  };
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("App", () => {
  it("shows the login form when there is no session", async () => {
    stubFetch({
      "GET /api/v1/auth/me": () => ({
        status: 200,
        body: { data: { authRequired: true, authenticated: false } },
      }),
    });
    render(<App />);
    expect(
      await screen.findByRole("heading", { name: "Sign in" }),
    ).toBeInTheDocument();
    expect(screen.getByLabelText("Password")).toHaveAttribute(
      "autocomplete",
      "current-password",
    );
  });

  it("announces a failed login", async () => {
    stubFetch({
      "GET /api/v1/auth/me": () => ({
        status: 200,
        body: { data: { authRequired: true, authenticated: false } },
      }),
      "POST /api/v1/auth/login": () => ({
        status: 401,
        body: { title: "Unauthorized", detail: "wrong username or password" },
      }),
    });
    render(<App />);
    const user = userEvent.setup();
    await user.type(await screen.findByLabelText("Username"), "diego");
    await user.type(screen.getByLabelText("Password"), "nope");
    await user.click(screen.getByRole("button", { name: "Sign in" }));
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Wrong username or password.",
    );
  });

  it("renders KPIs and the services table", async () => {
    stubFetch(dashboardRoutes);
    render(<App />);
    expect(await screen.findByText("99.50 %")).toBeInTheDocument();
    const row = (
      await screen.findByRole("button", {
        name: "Show response time for Bitso API",
      })
    ).closest("tr");
    if (!row) throw new Error("no row");
    expect(within(row).getByText("Operational")).toBeInTheDocument();
    expect(
      within(row).getByRole("img", {
        name: /Bitso API daily uptime: 90 days, 1 with data/,
      }),
    ).toBeInTheDocument();
  });

  it("shows API validation errors next to the field", async () => {
    const calls = stubFetch({
      ...dashboardRoutes,
      "POST /api/v1/checks": () => ({
        status: 422,
        body: {
          title: "Validation failed",
          errors: [
            {
              field: "target",
              message: "must be an absolute http or https URL",
            },
          ],
        },
      }),
    });
    render(<App />);
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "New check" }));
    await user.type(screen.getByLabelText("Name"), "API");
    await user.type(screen.getByLabelText("Target"), "api.example.com");
    await user.click(screen.getByRole("button", { name: "Create check" }));

    const target = screen.getByLabelText("Target");
    expect(
      await screen.findByText("Target must be an absolute http or https URL"),
    ).toBeInTheDocument();
    expect(target).toHaveAttribute("aria-invalid", "true");
    expect(target).toHaveFocus();
    const sent = calls.find((c) => c.key === "POST /api/v1/checks")
      ?.body as Record<string, unknown>;
    expect(sent).toMatchObject({
      name: "API",
      kind: "http",
      intervalS: 60,
      timeoutMs: 10000,
      enabled: true,
    });
    expect(sent).not.toHaveProperty("expectedStatus");
  });

  it("serves the public status page without asking for a login", async () => {
    window.history.replaceState(null, "", "/status");
    stubFetch({
      "GET /api/v1/status": () => ({
        status: 200,
        body: {
          data: {
            overall: "degraded",
            updatedAt: new Date().toISOString(),
            checks: [
              {
                id: 1,
                name: "Bitso API",
                kind: "http",
                status: "degraded",
                uptime90: 98.1,
                lastCheckedAt: null,
                daily: days,
              },
            ],
            incidents: [
              {
                id: 3,
                checkId: 1,
                name: "Bitso API",
                status: "degraded",
                startedAt: "2026-10-18T10:00:00Z",
                resolvedAt: "2026-10-18T10:25:00Z",
                updates: [
                  { at: "2026-10-18T10:00:00Z", status: "degraded" },
                  { at: "2026-10-18T10:25:00Z", status: "resolved" },
                ],
              },
            ],
          },
        },
      }),
    });
    render(<App />);
    expect(
      await screen.findByText("Some systems are degraded"),
    ).toBeInTheDocument();
    expect(screen.getByText("98.10 % uptime")).toBeInTheDocument();
    expect(screen.getByText(/lasted 25 min/)).toBeInTheDocument();
    expect(screen.queryByText(/api\.bitso\.com/)).not.toBeInTheDocument();
  });

  it("clears expectedStatus when an http check becomes dns", async () => {
    const httpCheck = { ...check, expectedStatus: 200 };
    const calls = stubFetch({
      ...dashboardRoutes,
      "GET /api/v1/checks": () => ({
        status: 200,
        body: { data: [httpCheck] },
      }),
      "PATCH /api/v1/checks/1": () => ({
        status: 200,
        body: { data: { ...check, kind: "dns" } },
      }),
    });
    render(<App />);
    const user = userEvent.setup();
    await user.click(
      await screen.findByRole("button", { name: "Edit Bitso API" }),
    );
    expect(screen.getByLabelText("Expected status")).toHaveValue(200);
    await user.selectOptions(screen.getByLabelText("Kind"), "dns");
    await user.clear(screen.getByLabelText("Target"));
    await user.type(screen.getByLabelText("Target"), "bitso.com");
    await user.click(screen.getByRole("button", { name: "Save changes" }));

    await screen.findByText("Saved Bitso API.");
    const sent = calls.find((c) => c.key === "PATCH /api/v1/checks/1")?.body;
    expect(sent).toMatchObject({
      kind: "dns",
      target: "bitso.com",
      expectedStatus: null,
    });
  });

  it("rejects a non-numeric interval before calling the API", async () => {
    const calls = stubFetch(dashboardRoutes);
    render(<App />);
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "New check" }));
    await user.type(screen.getByLabelText("Name"), "API");
    await user.type(screen.getByLabelText("Target"), "https://api.example.com");
    await user.clear(screen.getByLabelText("Interval (seconds)"));
    await user.click(screen.getByRole("button", { name: "Create check" }));
    expect(
      await screen.findByText("Interval must be a whole number of seconds"),
    ).toBeInTheDocument();
    expect(screen.getByLabelText("Interval (seconds)")).toHaveFocus();
    expect(calls.some((c) => c.key === "POST /api/v1/checks")).toBe(false);
  });

  it("shows a failed delete inside the dialog", async () => {
    stubFetch({
      ...dashboardRoutes,
      "DELETE /api/v1/checks/1": () => ({
        status: 500,
        body: { title: "Internal error" },
      }),
    });
    render(<App />);
    const user = userEvent.setup();
    await user.click(
      await screen.findByRole("button", { name: "Delete Bitso API" }),
    );
    await user.click(screen.getByRole("button", { name: "Delete check" }));
    const dialog = screen.getByRole("dialog", {
      name: "Delete Bitso API?",
      hidden: true,
    });
    expect(
      await within(dialog).findByRole("alert", { hidden: true }),
    ).toHaveTextContent("Could not delete: Internal error");
  });

  it("returns to the login form when the session expires", async () => {
    stubFetch({
      ...dashboardRoutes,
      "GET /api/v1/checks": () => ({
        status: 401,
        body: { title: "Unauthorized" },
      }),
    });
    render(<App />);
    expect(
      await screen.findByRole("heading", { name: "Sign in" }),
    ).toBeInTheDocument();
  });

  it("lists incidents with their errors and sends a test alert", async () => {
    const calls = stubFetch({
      ...dashboardRoutes,
      "POST /api/v1/alerts/test": () => ({ status: 204 }),
    });
    render(<App />);
    const user = userEvent.setup();
    const section = (
      await screen.findByRole("heading", { name: "Incidents" })
    ).closest("section");
    if (!section) throw new Error("no incidents section");
    expect(await within(section).findByText("· timeout")).toBeInTheDocument();
    expect(within(section).getByText(/ongoing for/)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Send test alert" }));
    expect(
      await screen.findByText("Test alert sent. Check the Telegram chat."),
    ).toBeInTheDocument();
    expect(calls.some((c) => c.key === "POST /api/v1/alerts/test")).toBe(true);
  });
  it("shows the AI diagnosis and starts an investigation", async () => {
    const summaryRoute = dashboardRoutes["GET /api/v1/summary"];
    if (!summaryRoute) throw new Error("no summary route");
    const summaryBody = summaryRoute(undefined).body as { data: object };
    let investigated = false;
    const calls = stubFetch({
      ...dashboardRoutes,
      "GET /api/v1/summary": () => ({
        status: 200,
        body: {
          data: {
            ...summaryBody.data,
            triage: { enabled: true, model: "claude-opus-5-5" },
          },
        },
      }),
      "GET /api/v1/incidents": () => ({
        status: 200,
        body: {
          data: [
            {
              id: 7,
              checkId: 1,
              checkName: "Bitso API",
              status: "down",
              startedAt: "2026-10-19T10:00:00Z",
              resolvedAt: null,
              updates: [
                {
                  at: "2026-10-19T10:00:00Z",
                  status: "down",
                  error: "timeout",
                },
              ],
              ...(investigated
                ? {
                    triage: {
                      id: 1,
                      incidentId: 7,
                      state: "running",
                      trigger: "manual",
                      model: "claude-opus-5-5",
                      evidence: [],
                      nextSteps: [],
                      toolCalls: [],
                      inputTokens: 0,
                      outputTokens: 0,
                      startedAt: "2026-10-19T10:05:00Z",
                      finishedAt: null,
                    },
                  }
                : {}),
            },
          ],
        },
      }),
      "POST /api/v1/incidents/7/triage": () => {
        investigated = true;
        return { status: 202, body: { data: { triageId: 1 } } };
      },
    });
    render(<App />);
    const user = userEvent.setup();
    expect(
      await screen.findByText(/AI triage: claude-opus-5-5/),
    ).toBeInTheDocument();
    await user.click(
      await screen.findByRole("button", {
        name: "Investigate with Claude (Bitso API)",
      }),
    );
    expect(
      await screen.findByText(/Claude is investigating: checking DNS/),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: /Investigate/ }),
    ).not.toBeInTheDocument();
    expect(calls.some((c) => c.key === "POST /api/v1/incidents/7/triage")).toBe(
      true,
    );
  });
});
