import { useEffect, useRef, type ReactNode } from "react";

/**
 * Native modal <dialog>: it traps focus, closes on Escape and makes the page
 * inert. Focus returns to whatever opened it or, when that element is gone
 * (a deleted row), to the page's <main>. While `busy`, Escape does nothing.
 */
export function Dialog({
  open,
  busy = false,
  onClose,
  labelledBy,
  children,
}: {
  open: boolean;
  busy?: boolean;
  onClose: () => void;
  labelledBy: string;
  children: ReactNode;
}) {
  const ref = useRef<HTMLDialogElement>(null);

  useEffect(() => {
    const d = ref.current;
    if (!d) return;
    if (open && !d.open) {
      const opener =
        document.activeElement instanceof HTMLElement
          ? document.activeElement
          : null;
      d.showModal();
      return () => {
        if (d.open) d.close();
        if (opener?.isConnected) opener.focus();
        else document.querySelector<HTMLElement>("main")?.focus();
      };
    }
    return undefined;
  }, [open]);

  return (
    <dialog
      ref={ref}
      aria-labelledby={labelledBy}
      aria-busy={busy || undefined}
      onCancel={(e) => {
        e.preventDefault();
        if (!busy) onClose();
      }}
      // The browser may still close it natively; keep React's state in step.
      onClose={() => {
        if (open) onClose();
      }}
    >
      {open && children}
    </dialog>
  );
}

export function ConfirmDialog({
  open,
  title,
  body,
  confirmLabel,
  busy,
  error,
  onConfirm,
  onClose,
}: {
  open: boolean;
  title: string;
  body: ReactNode;
  confirmLabel: string;
  busy: boolean;
  error: string;
  onConfirm: () => void;
  onClose: () => void;
}) {
  return (
    <Dialog
      open={open}
      busy={busy}
      onClose={onClose}
      labelledBy="confirm-title"
    >
      <div className="dialog-body">
        <h2 id="confirm-title">{title}</h2>
        <div className="muted">{body}</div>
        {/* Errors render inside the modal: the page behind it is inert. */}
        {error && (
          <p className="alert" role="alert">
            {error}
          </p>
        )}
        <div className="dialog-actions">
          {/* The safe choice gets the initial focus. */}
          <button
            type="button"
            className="btn"
            onClick={onClose}
            disabled={busy}
            autoFocus
          >
            Cancel
          </button>
          <button
            type="button"
            className="btn btn-danger"
            onClick={onConfirm}
            disabled={busy}
          >
            {busy ? "Deleting…" : confirmLabel}
          </button>
        </div>
      </div>
    </Dialog>
  );
}
