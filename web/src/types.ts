// Shapes returned by the Go API (internal/api). Keep in sync with the JSON tags.

export type Kind = "http" | "dns" | "tls";
export type Status = "operational" | "degraded" | "down" | "unknown";

export interface Result {
  id: number;
  checkId: number;
  startedAt: string;
  ok: boolean;
  statusCode?: number;
  totalMs: number;
  dnsMs?: number;
  connectMs?: number;
  tlsMs?: number;
  ttfbMs?: number;
  certExpiresAt?: string;
  error?: string;
}

export interface Check {
  id: number;
  name: string;
  kind: Kind;
  target: string;
  intervalS: number;
  timeoutMs: number;
  expectedStatus?: number;
  enabled: boolean;
  createdAt: string;
  updatedAt: string;
  status: Status;
  lastResult?: Result;
  uptime24h?: number;
  p95Ms24h?: number;
  uptime90?: number;
  /** 90 daily percentages, oldest first; null means no runs that day. */
  daily?: (number | null)[];
}

export interface CheckInput {
  name: string;
  kind: Kind;
  target: string;
  intervalS: number;
  timeoutMs: number;
  /** http only; omitted means any 2xx or 3xx. */
  expectedStatus?: number;
  enabled: boolean;
}

/** PATCH body: `expectedStatus: null` clears it. */
export type CheckPatch = Omit<CheckInput, "expectedStatus"> & {
  expectedStatus: number | null;
};

export interface Summary {
  /** AI incident triage: whether it is configured and the model it uses. */
  triage: { enabled: boolean; model?: string };
  openIncidents: number;
  /** Configured alert channels, for example ["telegram"]. */
  alertChannels: string[];
  checks: number;
  enabled: number;
  operational: number;
  degraded: number;
  down: number;
  unknown: number;
  uptime24h: number | null;
  p95Ms24h: number | null;
  runs24h: number;
}

export interface HourLatency {
  hour: string;
  total: number;
  ok: number;
  p50: number | null;
  p95: number | null;
}

export interface PublicCheck {
  id: number;
  name: string;
  kind: Kind;
  status: Status;
  uptime90: number | null;
  lastCheckedAt: string | null;
  daily: (number | null)[];
}

export type IncidentStatus = "degraded" | "down";

export interface IncidentUpdate {
  at: string;
  status: IncidentStatus | "resolved";
  /** Error category of the run; only the dashboard gets it. */
  error?: string;
}

export type TriageState = "running" | "done" | "failed";

export interface ToolCall {
  name: string;
  input?: string;
  outcome: string;
}

/** The newest AI investigation of an incident (dashboard only). */
export interface Triage {
  id: number;
  incidentId: number;
  state: TriageState;
  trigger: "auto" | "manual";
  model: string;
  summary?: string;
  probableCause?: string;
  confidence?: "low" | "medium" | "high";
  evidence: string[];
  nextSteps: string[];
  toolCalls: ToolCall[];
  inputTokens: number;
  outputTokens: number;
  error?: string;
  startedAt: string;
  finishedAt: string | null;
}

export interface Incident {
  id: number;
  checkId: number;
  checkName: string;
  status: IncidentStatus;
  startedAt: string;
  resolvedAt: string | null;
  updates: IncidentUpdate[];
  triage?: Triage;
}

export interface PublicIncident {
  id: number;
  checkId: number;
  name: string;
  status: IncidentStatus;
  startedAt: string;
  resolvedAt: string | null;
  updates: Omit<IncidentUpdate, "error">[];
}

export interface PublicStatus {
  overall: Exclude<Status, "unknown">;
  updatedAt: string;
  checks: PublicCheck[];
  incidents: PublicIncident[];
}

export interface Me {
  authRequired: boolean;
  authenticated: boolean;
  username?: string;
}

export interface FieldError {
  field: string;
  message: string;
}
