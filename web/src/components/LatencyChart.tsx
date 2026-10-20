import {
  useEffect,
  useId,
  useRef,
  useState,
  type KeyboardEvent,
  type PointerEvent,
} from "react";
import { hourLabel, ms } from "../format";
import type { HourLatency } from "../types";

const H = 240;
const M = { top: 12, right: 72, bottom: 28, left: 56 };
const PLOT_H = H - M.top - M.bottom;

type Key = "p50" | "p95";
const SERIES: { key: Key; label: string; color: string }[] = [
  { key: "p50", label: "p50", color: "var(--p50)" },
  { key: "p95", label: "p95", color: "var(--p95)" },
];

/** Rounds up to 1, 2 or 5 times a power of ten so the axis has clean ticks. */
function niceMax(v: number): number {
  if (v <= 0) return 100;
  const p = 10 ** Math.floor(Math.log10(v));
  for (const m of [1, 2, 5, 10]) if (v <= m * p) return m * p;
  return 10 * p;
}

/** One SVG path with a gap wherever an hour has no successful runs. */
function linePath(
  values: (number | null)[],
  x: (i: number) => number,
  y: (v: number) => number,
): string {
  let d = "";
  let pen = false;
  values.forEach((v, i) => {
    if (v === null) {
      pen = false;
      return;
    }
    d += `${pen ? "L" : "M"}${x(i).toFixed(1)},${y(v).toFixed(1)}`;
    pen = true;
  });
  return d;
}

export function LatencyChart({
  data,
  title,
}: {
  data: HourLatency[];
  title: string;
}) {
  const [active, setActive] = useState<number | null>(null);
  const [showTable, setShowTable] = useState(false);
  const tableId = useId();
  // Draw in real pixels so the axis text keeps its size on a phone.
  const box = useRef<HTMLDivElement>(null);
  const [W, setW] = useState(720);
  useEffect(() => {
    const el = box.current;
    if (!el) return;
    const ro = new ResizeObserver(([entry]) => {
      if (entry) setW(Math.max(280, Math.round(entry.contentRect.width)));
    });
    ro.observe(el);
    return () => {
      ro.disconnect();
    };
  }, []);
  const PLOT_W = W - M.left - M.right;
  const labelEvery = W < 520 ? 12 : 6;

  const n = data.length;
  const max = niceMax(Math.max(0, ...data.map((d) => d.p95 ?? 0)));
  const x = (i: number) =>
    M.left + (n <= 1 ? PLOT_W / 2 : (i / (n - 1)) * PLOT_W);
  const y = (v: number) => M.top + PLOT_H - (v / max) * PLOT_H;
  const ticks = [0, 0.25, 0.5, 0.75, 1].map((f) => f * max);
  const hasData = data.some((d) => d.p50 !== null);
  const lastIdx = (key: Key) => data.findLastIndex((d) => d[key] !== null);

  const pick = (e: PointerEvent<SVGRectElement>) => {
    const box = e.currentTarget.getBoundingClientRect();
    const f = (e.clientX - box.left) / box.width;
    setActive(Math.max(0, Math.min(n - 1, Math.round(f * (n - 1)))));
  };
  const onKey = (e: KeyboardEvent<SVGSVGElement>) => {
    const clamp = (i: number) => Math.max(0, Math.min(n - 1, i));
    if (e.key === "ArrowRight" || e.key === "ArrowLeft") {
      e.preventDefault();
      const step = e.key === "ArrowRight" ? 1 : -1;
      // From no selection, the first ArrowLeft lands on the latest hour.
      setActive((a) => clamp((a ?? n) + step));
    } else if (e.key === "Home" || e.key === "End") {
      e.preventDefault();
      setActive(e.key === "Home" ? 0 : n - 1);
    } else if (e.key === "Escape") {
      setActive(null);
    }
  };

  const point = active === null ? undefined : data[active];
  const spoken = point
    ? `${hourLabel(point.hour)}: p50 ${ms(point.p50)}, p95 ${ms(point.p95)}, ${point.ok} of ${point.total} runs ok`
    : "";

  return (
    <div className="chart" ref={box}>
      <div className="legend" style={{ marginBottom: 8 }}>
        {SERIES.map((s) => (
          <span key={s.key}>
            <i
              className="swatch swatch-line"
              style={{ background: s.color }}
              aria-hidden="true"
            />
            {s.label} latency
          </span>
        ))}
        <span className="subtle">successful runs, per hour (local time)</span>
      </div>

      {!hasData ? (
        <p className="empty">No successful runs in this window yet.</p>
      ) : (
        <svg
          viewBox={`0 0 ${W} ${H}`}
          role="group"
          aria-roledescription="chart"
          aria-label={`${title}. Use the arrow keys, Home and End to read each hour.`}
          tabIndex={0}
          onKeyDown={onKey}
          onBlur={() => {
            setActive(null);
          }}
        >
          <g className="grid axis">
            {ticks.map((t) => (
              <g key={t}>
                <line x1={M.left} x2={W - M.right} y1={y(t)} y2={y(t)} />
                <text x={M.left - 8} y={y(t)} dy="0.32em" textAnchor="end">
                  {ms(t)}
                </text>
              </g>
            ))}
          </g>
          <g className="axis">
            {data.map((d, i) =>
              i % labelEvery === 0 || i === n - 1 ? (
                <text key={d.hour} x={x(i)} y={H - 8} textAnchor="middle">
                  {hourLabel(d.hour)}
                </text>
              ) : null,
            )}
          </g>

          {SERIES.map((s) => (
            <path
              key={s.key}
              className="series"
              stroke={s.color}
              d={linePath(
                data.map((d) => d[s.key]),
                x,
                y,
              )}
            />
          ))}

          {SERIES.map((s) => {
            const i = lastIdx(s.key);
            const v = i >= 0 ? data[i]?.[s.key] : null;
            if (v === null || v === undefined) return null;
            return (
              <text
                key={s.key}
                className="end-label"
                x={x(i) + 8}
                y={y(v)}
                dy="0.32em"
              >
                {s.label} {ms(v)}
              </text>
            );
          })}

          {point && active !== null && (
            <g aria-hidden="true">
              <line
                className="crosshair"
                x1={x(active)}
                x2={x(active)}
                y1={M.top}
                y2={M.top + PLOT_H}
              />
              {SERIES.map((s) => {
                const v = point[s.key];
                return v === null ? null : (
                  <circle
                    key={s.key}
                    className="dot"
                    cx={x(active)}
                    cy={y(v)}
                    r={4}
                    fill={s.color}
                  />
                );
              })}
            </g>
          )}

          <rect
            x={M.left}
            y={M.top}
            width={PLOT_W}
            height={PLOT_H}
            fill="transparent"
            onPointerMove={pick}
            onPointerLeave={() => {
              setActive(null);
            }}
          />
        </svg>
      )}

      {point && active !== null && (
        <div
          className="tooltip"
          aria-hidden="true"
          style={{
            left: `${(x(active) / W) * 100}%`,
            transform:
              x(active) > W / 2
                ? "translateX(calc(-100% - 12px))"
                : "translateX(12px)",
          }}
        >
          <strong className="num">{hourLabel(point.hour)}</strong>
          <dl>
            <dt>p50</dt>
            <dd className="num">{ms(point.p50)}</dd>
            <dt>p95</dt>
            <dd className="num">{ms(point.p95)}</dd>
            <dt>runs</dt>
            <dd className="num">
              {point.ok}/{point.total} ok
            </dd>
          </dl>
        </div>
      )}

      {/* Always mounted, so screen readers announce each change. */}
      <p className="sr-only" aria-live="polite">
        {spoken}
      </p>

      <button
        type="button"
        className="link-btn"
        style={{ marginTop: 8, fontSize: 12 }}
        aria-expanded={showTable}
        aria-controls={tableId}
        onClick={() => {
          setShowTable((v) => !v);
        }}
      >
        {showTable ? "Hide table" : "Show as table"}
      </button>
      {showTable && (
        <div className="table-wrap" id={tableId}>
          <table>
            <caption className="sr-only">{title}</caption>
            <thead>
              <tr>
                <th scope="col">Hour</th>
                <th scope="col">p50</th>
                <th scope="col">p95</th>
                <th scope="col">Runs ok</th>
              </tr>
            </thead>
            <tbody>
              {data.map((d) => (
                <tr key={d.hour}>
                  <td className="num">{hourLabel(d.hour)}</td>
                  <td className="num">{ms(d.p50)}</td>
                  <td className="num">{ms(d.p95)}</td>
                  <td className="num">
                    {d.ok}/{d.total}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}
