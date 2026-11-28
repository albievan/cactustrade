import type { ReactNode } from "react";
import { usePref } from "../lib/prefs";

// A card whose body folds away behind its title. Whether it is open is the user's
// preference per card (R50: "infoCard.<id>.open"), so it stays folded at the next sign-in,
// in any browser.
export function InfoCard({ id, title, children }: { id: string; title: string; children: ReactNode }) {
  const key = "cactustrade.info." + id;
  const [open, setOpen] = usePref<boolean>(`infoCard.${id}.open`, true);
  return (
    <div className="card mb-4">
      <button type="button" className="card-header info-card-toggle d-flex align-items-center gap-2 border-0 text-start"
        aria-expanded={open} aria-controls={key} onClick={() => setOpen((o) => !o)}>
        <i className={`bi ${open ? "bi-chevron-down" : "bi-chevron-right"}`} aria-hidden="true" />
        <span className="h5 mb-0 flex-grow-1">{title}</span>
        <span className="small text-secondary">{open ? "Hide" : "Show"}</span>
      </button>
      {open && <div className="card-body" id={key}>{children}</div>}
    </div>
  );
}
