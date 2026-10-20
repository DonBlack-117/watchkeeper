import { Activity } from "lucide-react";
import { useEffect } from "react";
import { api } from "../api";
import { StatusBadge } from "../components/StatusBadge";
import { BarsLegend, UptimeBars } from "../components/UptimeBars";
import { ago, overallLabel, percent } from "../format";
import { usePoll } from "../usePoll";

/** Public, light-themed status page. Uses only /api/v1/status, which never exposes targets. */
export function StatusPage() {
  const { data, error, loading } = usePoll(api.status, 60_000);

  useEffect(() => {
    document.title = "Status · Watchkeeper";
  }, []);

  return (
    <div className="theme-light app-bg">
      <main className="status-page">
        <header className="status-head">
          <div className="brand" style={{ padding: 0 }}>
            <Activity size={20} aria-hidden="true" />
            Watchkeeper status
          </div>
          {data && (
            <span className="subtle">
              Updated {ago(data.updatedAt)}
              {error ? " · could not refresh" : ""}
            </span>
          )}
        </header>

        {error && !data && (
          <p className="alert" role="alert">
            The status could not be loaded. {error.message}
          </p>
        )}
        {loading && !data && !error && (
          <p className="muted" aria-busy="true">
            Loading status…
          </p>
        )}

        {data && (
          <>
            <section
              className={`card banner banner-${data.overall}`}
              aria-live="polite"
            >
              <StatusBadge status={data.overall} size={22} iconOnly />
              <span>{overallLabel[data.overall]}</span>
            </section>

            <section className="card" aria-labelledby="components-title">
              <div className="card-head">
                <h2 id="components-title">Services</h2>
                <span className="subtle" style={{ fontSize: 12 }}>
                  Last 90 days
                </span>
              </div>
              <div style={{ paddingTop: 8 }}>
                {data.checks.length === 0 && (
                  <p className="empty">No public services yet.</p>
                )}
                {data.checks.map((c) => (
                  <article className="component" key={c.id}>
                    <div className="component-head">
                      <h3 style={{ fontSize: 14 }}>{c.name}</h3>
                      <StatusBadge status={c.status} />
                    </div>
                    <UptimeBars
                      daily={c.daily}
                      label={`${c.name} daily uptime`}
                    />
                    <div className="component-foot">
                      <span>90 days ago</span>
                      <span className="num">{percent(c.uptime90)} uptime</span>
                      <span>Today</span>
                    </div>
                  </article>
                ))}
              </div>
            </section>
            <BarsLegend />
          </>
        )}
        <footer className="subtle" style={{ fontSize: 12 }}>
          Days are UTC. Checked by{" "}
          <a href="https://github.com/DonBlack-117/watchkeeper">Watchkeeper</a>.
        </footer>
      </main>
    </div>
  );
}
