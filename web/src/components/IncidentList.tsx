import { CheckCircle2 } from "lucide-react";
import { dateTime, duration } from "../format";
import type { IncidentUpdate, IncidentStatus, Triage } from "../types";
import { StatusBadge } from "./StatusBadge";
import { TriagePanel } from "./TriagePanel";

export interface IncidentItem {
  id: number;
  name: string;
  status: IncidentStatus;
  startedAt: string;
  resolvedAt: string | null;
  updates: IncidentUpdate[];
  triage?: Triage;
}

/** Dashboard-only AI triage controls; the public page passes none. */
export interface TriageControls {
  enabled: boolean;
  starting: ReadonlySet<number>;
  onInvestigate: (incidentId: number) => void;
}

const updateLabel: Record<IncidentUpdate["status"], string> = {
  degraded: "Degraded",
  down: "Down",
  resolved: "Resolved",
};

/**
 * Incidents newest first, each with its timeline. The dashboard passes updates
 * with error categories; the public page never has them.
 */
export function IncidentList({
  incidents,
  empty,
  triage,
}: {
  incidents: IncidentItem[];
  empty: string;
  triage?: TriageControls;
}) {
  if (incidents.length === 0) {
    return (
      <p className="incident-empty">
        <CheckCircle2 size={16} aria-hidden="true" />
        {empty}
      </p>
    );
  }
  return (
    <ol className="incidents">
      {incidents.map((inc) => (
        <li key={inc.id} className="incident">
          <div className="incident-head">
            <h3>{inc.name}</h3>
            {inc.resolvedAt ? (
              <span className="badge badge-operational">
                <CheckCircle2 size={16} aria-hidden="true" />
                Resolved
              </span>
            ) : (
              <StatusBadge status={inc.status} />
            )}
          </div>
          <p className="subtle incident-meta">
            {dateTime(inc.startedAt)} ·{" "}
            {inc.resolvedAt
              ? `lasted ${duration(inc.startedAt, inc.resolvedAt)}`
              : `ongoing for ${duration(inc.startedAt)}`}
          </p>
          <ol className="timeline" aria-label={`Updates for ${inc.name}`}>
            {inc.updates.map((u, i) => (
              <li key={i}>
                <span className={`dot dot-${u.status}`} aria-hidden="true" />
                <span className="num subtle">{dateTime(u.at)}</span>
                <span>{updateLabel[u.status]}</span>
                {u.error ? <span className="subtle">· {u.error}</span> : null}
              </li>
            ))}
          </ol>
          {triage && (
            <TriagePanel
              name={inc.name}
              triage={inc.triage}
              canInvestigate={triage.enabled}
              starting={triage.starting.has(inc.id)}
              onInvestigate={() => {
                triage.onInvestigate(inc.id);
              }}
            />
          )}
        </li>
      ))}
    </ol>
  );
}
