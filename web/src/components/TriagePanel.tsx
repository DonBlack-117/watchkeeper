import { LoaderCircle, Sparkles } from "lucide-react";
import { dateTime } from "../format";
import type { Triage } from "../types";

const toolLabel: Record<string, string> = {
  recent_results: "Recent runs",
  latency_by_hour: "Latency by hour",
  past_incidents: "Past incidents",
  dns_lookup: "DNS lookup",
  probe_now: "Live probe",
  inspect_tls: "TLS certificate",
};

/**
 * The AI diagnosis of one incident: running, finished or failed, with a
 * button to start or repeat it when triage is configured.
 */
export function TriagePanel({
  name,
  triage,
  canInvestigate,
  starting,
  onInvestigate,
}: {
  name: string;
  triage: Triage | undefined;
  canInvestigate: boolean;
  starting: boolean;
  onInvestigate: () => void;
}) {
  const running = triage?.state === "running" || starting;
  const label = triage ? "Investigate again" : "Investigate with Claude";
  const button = canInvestigate && !running && (
    <button
      type="button"
      className="btn btn-small"
      aria-label={`${label} (${name})`}
      onClick={onInvestigate}
    >
      <Sparkles size={14} aria-hidden="true" />
      {label}
    </button>
  );

  if (!triage && !starting) {
    return button ? <div className="triage-actions">{button}</div> : null;
  }

  return (
    <section className="triage" aria-label={`AI diagnosis for ${name}`}>
      <div className="triage-head">
        <span className="triage-title">
          <Sparkles size={14} aria-hidden="true" />
          AI diagnosis
        </span>
        {triage?.state === "done" && triage.confidence && (
          <span className={`confidence confidence-${triage.confidence}`}>
            {triage.confidence} confidence
          </span>
        )}
        {button}
      </div>

      <div aria-live="polite">
        {running ? (
          <p className="triage-running">
            <LoaderCircle size={14} className="spin" aria-hidden="true" />
            Claude is investigating: checking DNS, the endpoint and the history.
          </p>
        ) : triage?.state === "failed" ? (
          <p className="triage-failed">
            The investigation failed: {triage.error ?? "unknown error"}.
          </p>
        ) : triage ? (
          <>
            <p className="triage-summary">{triage.summary}</p>
            {triage.probableCause && (
              <p className="triage-cause">
                <span className="subtle">Probable cause: </span>
                {triage.probableCause}
              </p>
            )}
            {triage.nextSteps.length > 0 && (
              <>
                <h4>Next steps</h4>
                <ol className="triage-list">
                  {triage.nextSteps.map((step, i) => (
                    <li key={i}>{step}</li>
                  ))}
                </ol>
              </>
            )}
          </>
        ) : null}
      </div>

      {triage && !running && (
        <details className="triage-details">
          <summary>
            Evidence and tools ({triage.toolCalls.length} call
            {triage.toolCalls.length === 1 ? "" : "s"})
          </summary>
          {triage.evidence.length > 0 && (
            <>
              <h4>Evidence</h4>
              <ul className="triage-list">
                {triage.evidence.map((e, i) => (
                  <li key={i}>{e}</li>
                ))}
              </ul>
            </>
          )}
          {triage.toolCalls.length > 0 && (
            <>
              <h4>Tools used</h4>
              <ol className="triage-tools">
                {triage.toolCalls.map((c, i) => (
                  <li key={i}>
                    <span>{toolLabel[c.name] ?? c.name}</span>
                    {c.input && <code className="subtle">{c.input}</code>}
                    <span className="subtle">→ {c.outcome}</span>
                  </li>
                ))}
              </ol>
            </>
          )}
          <p className="subtle triage-meta">
            {triage.model} ·{" "}
            {triage.trigger === "auto" ? "automatic" : "manual"} ·{" "}
            {dateTime(triage.startedAt)} ·{" "}
            {(triage.inputTokens + triage.outputTokens).toLocaleString("en")}{" "}
            tokens
          </p>
        </details>
      )}
    </section>
  );
}
