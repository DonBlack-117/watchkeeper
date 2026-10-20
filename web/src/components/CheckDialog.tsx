import { useRef, useState, type SyntheticEvent } from "react";
import { ApiError, api } from "../api";
import type { Check, CheckInput, FieldError, Kind } from "../types";
import { Dialog } from "./Dialog";

const targetHint: Record<Kind, string> = {
  http: "Full URL, for example https://api.example.com/health",
  dns: "Hostname to resolve, for example example.com",
  tls: "host or host:port, for example example.com:443",
};

interface FormState {
  name: string;
  kind: Kind;
  target: string;
  intervalS: string;
  timeoutMs: string;
  expectedStatus: string;
  enabled: boolean;
}

function initial(c?: Check): FormState {
  return {
    name: c?.name ?? "",
    kind: c?.kind ?? "http",
    target: c?.target ?? "",
    intervalS: String(c?.intervalS ?? 60),
    timeoutMs: String(c?.timeoutMs ?? 10000),
    expectedStatus:
      c?.expectedStatus === undefined ? "" : String(c.expectedStatus),
    enabled: c?.enabled ?? true,
  };
}

const order = [
  "name",
  "kind",
  "target",
  "intervalS",
  "timeoutMs",
  "expectedStatus",
] as const;

/** Create or edit a check. Field errors from the API appear next to each field. */
export function CheckDialog({
  check,
  open,
  onClose,
  onSaved,
}: {
  check?: Check;
  open: boolean;
  onClose: () => void;
  onSaved: (c: Check) => void;
}) {
  const [busy, setBusy] = useState(false);
  return (
    <Dialog open={open} busy={busy} onClose={onClose} labelledBy="check-title">
      {/* Remount per check so the form starts from its values. */}
      <CheckForm
        key={check?.id ?? "new"}
        {...(check ? { check } : {})}
        onClose={onClose}
        onSaved={onSaved}
        onBusy={setBusy}
      />
    </Dialog>
  );
}

/** Client-side checks for what the server cannot see: empty or non-numeric numbers. */
function numberErrors(form: FormState): Record<string, string> {
  const out: Record<string, string> = {};
  const whole = (v: string) => /^\d+$/.test(v.trim());
  if (!whole(form.intervalS))
    out.intervalS = "must be a whole number of seconds";
  if (!whole(form.timeoutMs))
    out.timeoutMs = "must be a whole number of milliseconds";
  if (
    form.kind === "http" &&
    form.expectedStatus.trim() !== "" &&
    !whole(form.expectedStatus)
  )
    out.expectedStatus = "must be a number such as 200";
  return out;
}

function CheckForm({
  check,
  onClose,
  onSaved,
  onBusy,
}: {
  check?: Check;
  onClose: () => void;
  onSaved: (c: Check) => void;
  onBusy: (busy: boolean) => void;
}) {
  const [form, setForm] = useState<FormState>(() => initial(check));
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [formError, setFormError] = useState("");
  const [busy, setBusyState] = useState(false);
  const setBusy = (b: boolean) => {
    setBusyState(b);
    onBusy(b);
  };
  const formRef = useRef<HTMLFormElement>(null);

  const set = <K extends keyof FormState>(k: K, v: FormState[K]) => {
    setForm((f) => ({ ...f, [k]: v }));
  };

  const showErrors = (map: Record<string, string>) => {
    setErrors(map);
    const first = order.find(
      (f) => f in map && (f !== "expectedStatus" || form.kind === "http"),
    );
    if (first)
      formRef.current?.querySelector<HTMLElement>(`[name="${first}"]`)?.focus();
    // Errors for fields the form does not show still need to reach the user.
    const hidden = Object.entries(map).filter(
      ([f]) =>
        f !== first &&
        !order.some(
          (o) => o === f && (o !== "expectedStatus" || form.kind === "http"),
        ),
    );
    if (hidden.length > 0)
      setFormError(hidden.map(([f, m]) => `${label(f)} ${m}.`).join(" "));
  };

  const submit = async (e: SyntheticEvent) => {
    e.preventDefault();
    setErrors({});
    setFormError("");
    const local = numberErrors(form);
    if (Object.keys(local).length > 0) {
      showErrors(local);
      return;
    }
    setBusy(true);
    const input: CheckInput = {
      name: form.name,
      kind: form.kind,
      target: form.target,
      intervalS: Number(form.intervalS),
      timeoutMs: Number(form.timeoutMs),
      enabled: form.enabled,
    };
    if (form.kind === "http" && form.expectedStatus.trim() !== "") {
      input.expectedStatus = Number(form.expectedStatus);
    }
    try {
      // On edit, null clears an expected status that was removed or no longer applies.
      const saved = check
        ? await api.updateCheck(check.id, {
            ...input,
            expectedStatus: input.expectedStatus ?? null,
          })
        : await api.createCheck(input);
      setBusy(false);
      onSaved(saved);
      return;
    } catch (err) {
      if (err instanceof ApiError && err.fields.length > 0) {
        showErrors(
          Object.fromEntries(
            err.fields.map((f: FieldError) => [f.field, f.message]),
          ),
        );
      } else {
        setFormError(
          err instanceof Error ? err.message : "Could not save the check.",
        );
      }
    } finally {
      setBusy(false);
    }
  };

  const field = (name: (typeof order)[number]) => ({
    name,
    id: `f-${name}`,
    "aria-invalid": errors[name] ? true : undefined,
    "aria-describedby":
      [errors[name] ? `e-${name}` : "", name === "target" ? "h-target" : ""]
        .filter(Boolean)
        .join(" ") || undefined,
  });
  const err = (name: string) =>
    errors[name] ? (
      <span className="error" id={`e-${name}`}>
        {label(name)} {errors[name]}
      </span>
    ) : null;

  return (
    <form
      ref={formRef}
      className="dialog-body"
      onSubmit={(e) => void submit(e)}
      noValidate
    >
      <h2 id="check-title">{check ? `Edit ${check.name}` : "New check"}</h2>
      {formError && (
        <p className="alert" role="alert">
          {formError}
        </p>
      )}
      <div className="form-grid">
        <div className="field span-2">
          <label htmlFor="f-name">Name</label>
          <input
            {...field("name")}
            value={form.name}
            onChange={(e) => {
              set("name", e.target.value);
            }}
            maxLength={100}
            required
            autoComplete="off"
          />
          {err("name")}
        </div>
        <div className="field">
          <label htmlFor="f-kind">Kind</label>
          <select
            {...field("kind")}
            value={form.kind}
            onChange={(e) => {
              set("kind", e.target.value as Kind);
            }}
          >
            <option value="http">HTTP</option>
            <option value="dns">DNS</option>
            <option value="tls">TLS certificate</option>
          </select>
          {err("kind")}
        </div>
        <div className="field">
          <label htmlFor="f-intervalS">Interval (seconds)</label>
          <input
            {...field("intervalS")}
            type="number"
            inputMode="numeric"
            min={30}
            max={86400}
            value={form.intervalS}
            onChange={(e) => {
              set("intervalS", e.target.value);
            }}
          />
          {err("intervalS")}
        </div>
        <div className="field span-2">
          <label htmlFor="f-target">Target</label>
          <input
            {...field("target")}
            value={form.target}
            onChange={(e) => {
              set("target", e.target.value);
            }}
            spellCheck={false}
            autoComplete="off"
            required
            maxLength={2048}
          />
          <span className="hint" id="h-target">
            {targetHint[form.kind]}
          </span>
          {err("target")}
        </div>
        <div className="field">
          <label htmlFor="f-timeoutMs">Timeout (ms)</label>
          <input
            {...field("timeoutMs")}
            type="number"
            inputMode="numeric"
            min={1000}
            max={30000}
            value={form.timeoutMs}
            onChange={(e) => {
              set("timeoutMs", e.target.value);
            }}
          />
          {err("timeoutMs")}
        </div>
        {form.kind === "http" && (
          <div className="field">
            <label htmlFor="f-expectedStatus">Expected status</label>
            <input
              {...field("expectedStatus")}
              type="number"
              inputMode="numeric"
              min={100}
              max={599}
              placeholder="Any 2xx or 3xx"
              value={form.expectedStatus}
              onChange={(e) => {
                set("expectedStatus", e.target.value);
              }}
            />
            {err("expectedStatus")}
          </div>
        )}
        <label className="check-row span-2">
          <input
            type="checkbox"
            checked={form.enabled}
            onChange={(e) => {
              set("enabled", e.target.checked);
            }}
          />
          Enabled
        </label>
      </div>
      <div className="dialog-actions">
        <button type="button" className="btn" onClick={onClose} disabled={busy}>
          Cancel
        </button>
        <button type="submit" className="btn btn-primary" disabled={busy}>
          {busy ? "Saving…" : check ? "Save changes" : "Create check"}
        </button>
      </div>
    </form>
  );
}

function label(field: string): string {
  const names: Record<string, string> = {
    intervalS: "Interval",
    timeoutMs: "Timeout",
    expectedStatus: "Expected status",
  };
  return names[field] ?? field.charAt(0).toUpperCase() + field.slice(1);
}
