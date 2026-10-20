import {
  Activity,
  ExternalLink,
  LayoutDashboard,
  LogOut,
  Pencil,
  Plus,
  Send,
  Trash2,
} from "lucide-react";
import { useCallback, useEffect, useState } from "react";
import { api } from "../api";
import { CheckDialog } from "../components/CheckDialog";
import { ConfirmDialog } from "../components/Dialog";
import { LatencyChart } from "../components/LatencyChart";
import { StatusBadge } from "../components/StatusBadge";
import { BarsLegend, UptimeBars } from "../components/UptimeBars";
import { ago, ms, percent } from "../format";
import type { Check } from "../types";
import { usePoll } from "../usePoll";
import { IncidentList } from "../components/IncidentList";

const REFRESH_MS = 30_000;
const EMPTY_DAYS: null[] = Array<null>(90).fill(null);

export function Dashboard({
  username,
  onLogout,
}: {
  username: string | undefined;
  onLogout: (() => Promise<void>) | undefined;
}) {
  const checks = usePoll(api.checks, REFRESH_MS);
  const summary = usePoll(api.summary, REFRESH_MS);
  const incidentPoll = usePoll(api.incidents, REFRESH_MS);
  const [testing, setTesting] = useState(false);
  const [selectedId, setSelectedId] = useState<number>();
  const [editing, setEditing] = useState<Check | "new">();
  const [deleting, setDeleting] = useState<Check>();
  const [busy, setBusy] = useState(false);
  const [deleteError, setDeleteError] = useState("");
  // An id per message, so the same text twice still resets the timer and is announced.
  const [toast, setToastState] = useState<{ id: number; text: string }>();
  const setToast = (text: string) => {
    setToastState((prev) => ({ id: (prev?.id ?? 0) + 1, text }));
  };

  const list = checks.data ?? [];
  const selected =
    list.find((c) => c.id === selectedId) ??
    list.find((c) => c.enabled) ??
    list[0];
  const latency = usePoll(
    (signal) =>
      selected ? api.latency(selected.id, signal) : Promise.resolve([]),
    REFRESH_MS,
    selected?.id,
  );

  useEffect(() => {
    document.title = "Dashboard · Watchkeeper";
  }, []);

  useEffect(() => {
    if (!toast) return;
    const t = setTimeout(() => {
      setToastState(undefined);
    }, 4000);
    return () => {
      clearTimeout(t);
    };
  }, [toast]);

  const { reload: reloadChecks } = checks;
  const { reload: reloadSummary } = summary;
  const { reload: reloadLatency } = latency;
  const { reload: reloadIncidents } = incidentPoll;
  const reloadAll = useCallback(() => {
    reloadChecks();
    reloadSummary();
    reloadLatency();
    reloadIncidents();
  }, [reloadChecks, reloadSummary, reloadLatency, reloadIncidents]);

  const remove = async () => {
    if (!deleting) return;
    setBusy(true);
    setDeleteError("");
    try {
      await api.deleteCheck(deleting.id);
      setToast(`Deleted ${deleting.name}.`);
      if (selectedId === deleting.id) setSelectedId(undefined);
      setDeleting(undefined);
      reloadAll();
    } catch (e) {
      // Shown inside the dialog: the page behind the modal is inert.
      setDeleteError(
        e instanceof Error
          ? `Could not delete: ${e.message}`
          : "Could not delete the check.",
      );
    } finally {
      setBusy(false);
    }
  };

  const logout = onLogout
    ? () => {
        onLogout().catch((e: unknown) => {
          setToast(
            e instanceof Error
              ? `Could not sign out: ${e.message}`
              : "Could not sign out.",
          );
        });
      }
    : undefined;

  const s = summary.data;
  const alertsOn = (s?.alertChannels.length ?? 0) > 0;

  const testAlert = async () => {
    setTesting(true);
    try {
      await api.testAlert();
      setToast("Test alert sent. Check the Telegram chat.");
    } catch (e) {
      setToast(
        e instanceof Error
          ? `Test alert failed: ${e.message}`
          : "Test alert failed.",
      );
    } finally {
      setTesting(false);
    }
  };

  return (
    <div className="shell app-bg">
      <aside className="sidebar" aria-label="Main">
        <div className="brand">
          <Activity size={20} aria-hidden="true" />
          Watchkeeper
        </div>
        <nav className="nav" aria-label="Sections">
          <a href="/" aria-current="page">
            <LayoutDashboard size={16} aria-hidden="true" />
            <span>Dashboard</span>
          </a>
          <a href="/status" target="_blank" rel="noreferrer">
            <ExternalLink size={16} aria-hidden="true" />
            <span>
              Status page<span className="sr-only"> (opens in a new tab)</span>
            </span>
          </a>
        </nav>
        {logout && (
          <div className="sidebar-foot">
            {username && <span className="subtle">{username}</span>}
            <button type="button" className="btn" onClick={logout}>
              <LogOut size={14} aria-hidden="true" />
              Sign out
            </button>
          </div>
        )}
      </aside>

      {/* tabIndex lets dialogs return focus here when their opener is gone. */}
      <main className="main" tabIndex={-1}>
        <div className="page-head">
          <div>
            <h1>Overview</h1>
            <p className="muted" style={{ margin: 0 }}>
              Last 24 hours, refreshed every 30 s.
            </p>
          </div>
          <button
            type="button"
            className="btn btn-primary"
            onClick={() => {
              setEditing("new");
            }}
          >
            <Plus size={16} aria-hidden="true" />
            New check
          </button>
        </div>

        {(checks.error ?? summary.error) && (
          <p className="alert" role="alert">
            Could not load the latest data:{" "}
            {(checks.error ?? summary.error)?.message}
          </p>
        )}

        <section aria-label="Key figures">
          <dl className="kpis" style={{ margin: 0 }}>
            <div className="card kpi">
              <dt>Availability</dt>
              <dd>{s ? percent(s.uptime24h) : "…"}</dd>
              <dd className="kpi-note">
                {s ? `${s.runs24h.toLocaleString("en")} runs` : ""}
              </dd>
            </div>
            <div className="card kpi">
              <dt>p95 latency</dt>
              <dd>{s ? ms(s.p95Ms24h) : "…"}</dd>
              <dd className="kpi-note">successful runs</dd>
            </div>
            <div className="card kpi">
              <dt>Open incidents</dt>
              <dd>{s ? s.openIncidents : "…"}</dd>
              <dd className="kpi-note">
                {s ? `${s.down} down, ${s.degraded} degraded` : ""}
              </dd>
            </div>
            <div className="card kpi">
              <dt>Checks</dt>
              <dd>{s ? s.checks : "…"}</dd>
              <dd className="kpi-note">{s ? `${s.enabled} enabled` : ""}</dd>
            </div>
          </dl>
        </section>

        <section className="card" aria-labelledby="latency-title">
          <div className="card-head">
            <h2 id="latency-title">Response time</h2>
            {list.length > 0 && (
              <label className="check-row">
                <span className="muted">Check</span>
                <select
                  className="select"
                  value={selected?.id ?? ""}
                  onChange={(e) => {
                    setSelectedId(Number(e.target.value));
                  }}
                >
                  {list.map((c) => (
                    <option key={c.id} value={c.id}>
                      {c.name}
                    </option>
                  ))}
                </select>
              </label>
            )}
          </div>
          <div className="card-body">
            {selected && latency.error && !latency.data ? (
              <p className="alert" role="alert">
                Could not load the response time: {latency.error.message}
              </p>
            ) : selected ? (
              latency.data ? (
                <LatencyChart
                  data={latency.data}
                  title={`${selected.name}: p50 and p95 latency per hour, last 24 hours`}
                />
              ) : (
                <p className="muted" aria-busy="true">
                  Loading…
                </p>
              )
            ) : (
              <p className="empty">Create a check to see its response time.</p>
            )}
          </div>
        </section>

        <section className="card" aria-labelledby="incidents-title">
          <div className="card-head">
            <h2 id="incidents-title">Incidents</h2>
            {s && (
              <div className="check-row">
                <span className="subtle" style={{ fontSize: 12 }}>
                  {alertsOn
                    ? `Alerts: ${s.alertChannels.join(", ")}`
                    : "Alerts off: set WK_TELEGRAM_BOT_TOKEN"}
                </span>
                {alertsOn && (
                  <button
                    type="button"
                    className="btn"
                    onClick={() => void testAlert()}
                    disabled={testing}
                  >
                    <Send size={14} aria-hidden="true" />
                    {testing ? "Sending…" : "Send test alert"}
                  </button>
                )}
              </div>
            )}
          </div>
          <div style={{ marginTop: 8 }}>
            {incidentPoll.error && !incidentPoll.data ? (
              <p className="alert" role="alert" style={{ margin: 18 }}>
                Could not load incidents: {incidentPoll.error.message}
              </p>
            ) : incidentPoll.data ? (
              <IncidentList
                incidents={incidentPoll.data.map((i) => ({
                  ...i,
                  name: i.checkName,
                }))}
                empty="No incidents yet."
              />
            ) : (
              <p className="empty" aria-busy="true">
                Loading incidents…
              </p>
            )}
          </div>
        </section>

        <section className="card" aria-labelledby="services-title">
          <div className="card-head">
            <h2 id="services-title">Services</h2>
            <BarsLegend />
          </div>
          <div className="table-wrap" style={{ marginTop: 8 }}>
            {checks.loading && !checks.data ? (
              <p className="empty" aria-busy="true">
                Loading checks…
              </p>
            ) : list.length === 0 ? (
              <div className="empty">
                <p>No checks yet.</p>
                <button
                  type="button"
                  className="btn btn-primary"
                  onClick={() => {
                    setEditing("new");
                  }}
                >
                  <Plus size={16} aria-hidden="true" />
                  Create the first check
                </button>
              </div>
            ) : (
              <table>
                <thead>
                  <tr>
                    <th scope="col">Service</th>
                    <th scope="col">Status</th>
                    <th scope="col" className="col-bars">
                      Last 90 days
                    </th>
                    <th scope="col">Uptime 90 d</th>
                    <th scope="col">p95 24 h</th>
                    <th scope="col">Last run</th>
                    <th scope="col">
                      <span className="sr-only">Actions</span>
                    </th>
                  </tr>
                </thead>
                <tbody>
                  {list.map((c) => (
                    <tr
                      key={c.id}
                      className={c.id === selected?.id ? "selected" : undefined}
                    >
                      <td>
                        <div className="svc-name">
                          <button
                            type="button"
                            onClick={() => {
                              setSelectedId(c.id);
                            }}
                            aria-label={`Show response time for ${c.name}`}
                            aria-pressed={c.id === selected?.id}
                          >
                            {c.name}
                          </button>
                          <span className="svc-target" title={c.target}>
                            {c.kind.toUpperCase()} · {c.target}
                          </span>
                        </div>
                      </td>
                      <td>
                        {c.enabled ? (
                          <StatusBadge status={c.status} />
                        ) : (
                          <span className="subtle">Paused</span>
                        )}
                      </td>
                      <td className="col-bars">
                        <UptimeBars
                          daily={c.daily ?? EMPTY_DAYS}
                          label={`${c.name} daily uptime`}
                        />
                      </td>
                      <td className="num">{percent(c.uptime90)}</td>
                      <td className="num">{ms(c.p95Ms24h)}</td>
                      <td
                        className="subtle nowrap"
                        title={c.lastResult?.error ?? ""}
                      >
                        {ago(c.lastResult?.startedAt)}
                        {c.lastResult?.error ? (
                          <span className="sr-only">
                            , error: {c.lastResult.error}
                          </span>
                        ) : null}
                      </td>
                      <td>
                        <div className="row-actions">
                          <button
                            type="button"
                            className="icon-btn"
                            aria-label={`Edit ${c.name}`}
                            title="Edit"
                            onClick={() => {
                              setEditing(c);
                            }}
                          >
                            <Pencil size={16} aria-hidden="true" />
                          </button>
                          <button
                            type="button"
                            className="icon-btn"
                            aria-label={`Delete ${c.name}`}
                            title="Delete"
                            onClick={() => {
                              setDeleting(c);
                            }}
                          >
                            <Trash2 size={16} aria-hidden="true" />
                          </button>
                        </div>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            )}
          </div>
        </section>
      </main>

      <CheckDialog
        open={editing !== undefined}
        {...(editing && editing !== "new" ? { check: editing } : {})}
        onClose={() => {
          setEditing(undefined);
        }}
        onSaved={(c) => {
          setToast(
            editing === "new"
              ? `Created ${c.name}. The first run starts within 30 s.`
              : `Saved ${c.name}.`,
          );
          setEditing(undefined);
          setSelectedId(c.id);
          reloadAll();
        }}
      />
      <ConfirmDialog
        open={deleting !== undefined}
        title={`Delete ${deleting?.name ?? ""}?`}
        body="The check and its whole history are deleted. This cannot be undone."
        confirmLabel="Delete check"
        busy={busy}
        error={deleteError}
        onConfirm={() => void remove()}
        onClose={() => {
          setDeleting(undefined);
          setDeleteError("");
        }}
      />
      <div className="toasts" role="status" aria-live="polite">
        {toast && (
          <div className="toast" key={toast.id}>
            {toast.text}
          </div>
        )}
      </div>
    </div>
  );
}
