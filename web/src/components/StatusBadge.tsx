import {
  AlertTriangle,
  CheckCircle2,
  CircleDashed,
  XCircle,
} from "lucide-react";
import { statusLabel } from "../format";
import type { Status } from "../types";

const icons = {
  operational: CheckCircle2,
  degraded: AlertTriangle,
  down: XCircle,
  unknown: CircleDashed,
} as const;

/** Status as icon plus text, never color alone. `iconOnly` is for places that print their own text next to it. */
export function StatusBadge({
  status,
  size = 16,
  iconOnly = false,
}: {
  status: Status;
  size?: number;
  iconOnly?: boolean;
}) {
  const Icon = icons[status];
  return (
    <span className={`badge badge-${status}`}>
      <Icon size={size} aria-hidden="true" />
      {!iconOnly && statusLabel[status]}
    </span>
  );
}
