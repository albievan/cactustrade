import { useState, type ReactNode } from "react";

// A card whose body folds away behind its title. The choice is remembered per card in this
// browser (a convenience only: without storage it simply starts open).
export function InfoCard({ id, title, children }: { id: string; title: string; children: ReactNode }) {
  const key = "cactustrade.info." + id;
  const [open, setOpen] = useState(() => {
    try { return localStorage.getItem(key) !== "closed"; } catch { return true; }
  });
  const toggle = () => {
    const next = !open;
    setOpen(next);
    try { localStorage.setItem(key, next ? "open" : "closed"); } catch { /* storage blocked: fine */ }
  };
  return (
    <div className="card mb-4">
      <button type="button" className="card-header info-card-toggle d-flex align-items-center gap-2 border-0 text-start"
        aria-expanded={open} aria-controls={key} onClick={toggle}>
        <i className={`bi ${open ? "bi-chevron-down" : "bi-chevron-right"}`} aria-hidden="true" />
        <span className="h5 mb-0 flex-grow-1">{title}</span>
        <span className="small text-secondary">{open ? "Hide" : "Show"}</span>
      </button>
      {open && <div className="card-body" id={key}>{children}</div>}
    </div>
  );
}
