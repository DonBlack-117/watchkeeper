import { dayLabel, dayLevel, percent } from "../format";

const BAR = 4;
const GAP = 2;

/**
 * One thin bar per UTC day, oldest on the left. Each bar has a native tooltip;
 * the whole strip is summarised for screen readers in its label.
 */
export function UptimeBars({
  daily,
  label,
}: {
  daily: (number | null)[];
  label: string;
}) {
  const n = daily.length;
  const width = n * BAR + (n - 1) * GAP;
  const withData = daily.filter((d): d is number => d !== null);
  const worst = withData.length ? Math.min(...withData) : null;
  const summary = withData.length
    ? `${label}: ${n} days, ${withData.length} with data, worst day ${percent(worst)}`
    : `${label}: no data in the last ${n} days`;

  return (
    <svg
      className="bars"
      viewBox={`0 0 ${width} 28`}
      preserveAspectRatio="none"
      role="img"
      aria-label={summary}
    >
      {daily.map((p, i) => (
        <rect
          key={i}
          className={dayLevel(p)}
          x={i * (BAR + GAP)}
          y={0}
          width={BAR}
          height={28}
          rx={1.5}
        >
          <title>{`${dayLabel(n - 1 - i)}: ${p === null ? "no data" : percent(p)}`}</title>
        </rect>
      ))}
    </svg>
  );
}

export function BarsLegend() {
  return (
    <div className="legend" aria-label="Bar colors">
      <span>
        <i
          className="swatch"
          style={{ background: "var(--ok)" }}
          aria-hidden="true"
        />
        ≥ 99.5 %
      </span>
      <span>
        <i
          className="swatch"
          style={{ background: "var(--slow)" }}
          aria-hidden="true"
        />
        ≥ 98 %
      </span>
      <span>
        <i
          className="swatch"
          style={{ background: "var(--degraded)" }}
          aria-hidden="true"
        />
        ≥ 90 %
      </span>
      <span>
        <i
          className="swatch"
          style={{ background: "var(--down)" }}
          aria-hidden="true"
        />
        &lt; 90 %
      </span>
      <span>
        <i
          className="swatch"
          style={{ background: "var(--none)" }}
          aria-hidden="true"
        />
        No data
      </span>
    </div>
  );
}
