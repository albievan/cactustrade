import { useEffect, useId, useState, type FormEvent, type ReactNode } from "react";

// ConfirmDialog asks before an action, as a Bootstrap modal drawn by React (no Bootstrap
// JavaScript). It replaces window.confirm, alert and prompt everywhere (ESLint bans them).
// Escape, the close button and Cancel call onCancel; the confirm button (or Enter in a field
// of the body) calls onConfirm once: while the action it returns runs, the button is disabled
// and shows busyLabel. The caller closes the dialog (on success, and on failure if it wants).
// Anything the action needs typed (a reason, a phrase, a password) goes in children.
export function ConfirmDialog({
  title, children, confirmLabel, busyLabel, icon, variant = "danger", confirmDisabled = false, busy = false, onCancel, onConfirm,
}: {
  title: string;
  children: ReactNode;
  confirmLabel: string;
  busyLabel?: string;
  icon?: string; // a Bootstrap icon name such as "trash"
  variant?: "danger" | "warning" | "primary" | "success";
  confirmDisabled?: boolean;
  busy?: boolean; // the caller's own busy state, if it has one
  onCancel: () => void;
  onConfirm: () => void | Promise<unknown>;
}) {
  const id = useId();
  const [running, setRunning] = useState(false);
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => { if (e.key === "Escape") onCancel(); };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onCancel]);
  const working = running || busy;
  async function submit(e: FormEvent) {
    e.preventDefault();
    if (working || confirmDisabled) return;
    setRunning(true);
    try {
      await onConfirm();
    } finally {
      setRunning(false);
    }
  }
  return (
    <>
      <div className="modal d-block" tabIndex={-1} role="dialog" aria-modal="true" aria-labelledby={id}>
        <div className="modal-dialog modal-dialog-centered">
          <div className="modal-content">
            <div className="modal-header">
              <h2 className="modal-title h5" id={id}>{title}</h2>
              <button type="button" className="btn-close" aria-label="Close" onClick={onCancel} />
            </div>
            <form onSubmit={submit}>
              <div className="modal-body">{children}</div>
              <div className="modal-footer">
                <button type="button" className="btn btn-outline-secondary" onClick={onCancel}>Cancel</button>
                <button type="submit" className={"btn btn-" + variant} disabled={working || confirmDisabled}>
                  {icon && <i className={"bi bi-" + icon + " me-1"} />}
                  {working && busyLabel ? busyLabel : confirmLabel}
                </button>
              </div>
            </form>
          </div>
        </div>
      </div>
      <div className="modal-backdrop show" />
    </>
  );
}
