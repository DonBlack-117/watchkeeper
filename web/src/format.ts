import type { Status } from "./types";

export function percent(v: number | null | undefined, digits = 2): string {
  if (v === null || v === undefined) return "—";
  // 99.999 should not round up to a misleading 100.00 %.
  const factor = 10 ** digits;
  return `${(Math.floor(v * factor) / factor).toFixed(digits)} %`;
}

export function ms(v: number | null | undefined): string {
  if (v === null || v === undefined) return "—";
  return v >= 1000 ? `${(v / 1000).toFixed(2)} s` : `${Math.round(v)} ms`;
}

const rtf = new Intl.RelativeTimeFormat("en", { numeric: "auto" });

export function ago(iso: string | null | undefined, now = Date.now()): string {
  if (!iso) return "never";
  const s = Math.round((new Date(iso).getTime() - now) / 1000);
  if (Math.abs(s) < 60) return rtf.format(s, "second");
  if (Math.abs(s) < 3600) return rtf.format(Math.round(s / 60), "minute");
  if (Math.abs(s) < 86400) return rtf.format(Math.round(s / 3600), "hour");
  return rtf.format(Math.round(s / 86400), "day");
}

/** "2 h 5 min", "6 min" or "40 s" between two ISO times (or until now). */
export function duration(
  fromIso: string,
  toIso?: string | null,
  now = Date.now(),
): string {
  const end = toIso ? new Date(toIso).getTime() : now;
  const s = Math.max(0, Math.round((end - new Date(fromIso).getTime()) / 1000));
  if (s < 60) return `${s} s`;
  if (s < 3600) return `${Math.floor(s / 60)} min`;
  if (s < 172800)
    return `${Math.floor(s / 3600)} h ${Math.floor((s % 3600) / 60)} min`;
  return `${Math.floor(s / 86400)} days ${Math.floor((s % 86400) / 3600)} h`;
}

export function dateTime(iso: string): string {
  return new Date(iso).toLocaleString("en-GB", {
    day: "numeric",
    month: "short",
    hour: "2-digit",
    minute: "2-digit",
  });
}

export function hourLabel(iso: string): string {
  return new Date(iso).toLocaleTimeString("en-GB", {
    hour: "2-digit",
    minute: "2-digit",
  });
}

export function dayLabel(offsetFromToday: number, now = new Date()): string {
  const d = new Date(
    Date.UTC(
      now.getUTCFullYear(),
      now.getUTCMonth(),
      now.getUTCDate() - offsetFromToday,
    ),
  );
  return d.toLocaleDateString("en-GB", {
    day: "numeric",
    month: "short",
    timeZone: "UTC",
  });
}

export const statusLabel: Record<Status, string> = {
  operational: "Operational",
  degraded: "Degraded",
  down: "Down",
  unknown: "No data",
};

export const overallLabel: Record<Exclude<Status, "unknown">, string> = {
  operational: "All systems operational",
  degraded: "Some systems are degraded",
  down: "Major outage",
};

/** Bucket for one day's bar: thresholds follow common status-page practice. */
export type DayLevel = "ok" | "slow" | "degraded" | "down" | "none";

export function dayLevel(p: number | null | undefined): DayLevel {
  if (p === null || p === undefined) return "none";
  if (p >= 99.5) return "ok";
  if (p >= 98) return "slow";
  if (p >= 90) return "degraded";
  return "down";
}
