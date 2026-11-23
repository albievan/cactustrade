import { useState } from "react";
import { NavLink, Outlet, useLocation } from "react-router-dom";
import { useSession } from "../lib/session";
import { useMode } from "../lib/mode";
import { EventsBell } from "./EventsBell";

interface Item { to: string; icon: string; label: string; perm?: string; end?: boolean }

const sections: { title: string; items: Item[] }[] = [
  { title: "Trading", items: [
    { to: "/", icon: "bi-speedometer2", label: "Dashboard", end: true },
    { to: "/watchlist", icon: "bi-binoculars", label: "Watchlist" },
    { to: "/risk", icon: "bi-shield-check", label: "Risk limits" },
  ] },
  { title: "Setup", items: [
    { to: "/credentials", icon: "bi-key", label: "eToro keys" },
    { to: "/llm", icon: "bi-robot", label: "LLM reviewer" },
    { to: "/api-keys", icon: "bi-plug", label: "API keys" },
  ] },
  { title: "Admin", items: [
    { to: "/users", icon: "bi-people", label: "Users", perm: "users.manage" },
    { to: "/audit", icon: "bi-journal-text", label: "Audit log", perm: "audit.view" },
    { to: "/system", icon: "bi-gear", label: "System", perm: "settings.write" },
  ] },
];

export function Layout() {
  const { me, can, signOut } = useSession();
  const mode = useMode();
  const real = mode?.mode === "real";
  const [navOpen, setNavOpen] = useState(false);
  const location = useLocation();
  return (
    <>
      <header className="app-head">
        <button className="btn btn-sm btn-outline-secondary side-toggle" onClick={() => setNavOpen(!navOpen)} aria-label="Menu"><i className="bi bi-list" /></button>
        <a className="brand" href="/">
          <img src="/logo.svg" alt="" />
          <span className="name">cactus<b>trade</b></span>
        </a>
        <span className="mode-pill" title="Trading mode (from the server)">
          {real ? <><i className="bi bi-exclamation-octagon-fill me-1" />REAL MONEY</> : <><i className="bi bi-mortarboard me-1" />DEMO · practice money</>}
        </span>
        <span className="flex-grow-1" />
        <EventsBell />
        <a className="btn btn-sm btn-outline-secondary" href="/api/docs" title="API documentation (Swagger UI)"><i className="bi bi-braces" /></a>
        <div className="dropdown">
          <NavLink className="btn btn-sm btn-outline-secondary" to="/account" title="Your account">
            <i className="bi bi-person-circle me-1" />{me?.user.username} <span className="opacity-75">({me?.user.role})</span>
          </NavLink>
        </div>
        <button className="btn btn-sm btn-outline-secondary" onClick={() => void signOut()} title="Sign out" aria-label="Sign out"><i className="bi bi-box-arrow-right" /></button>
      </header>
      {real && <div className="real-strip">REAL MONEY — orders use your live eToro account</div>}
      <div className="shell">
        <nav className={"side" + (navOpen ? " open" : "")} aria-label="Menu" onClick={() => setNavOpen(false)}>
          {sections.map((s) => {
            const items = s.items.filter((i) => !i.perm || can(i.perm));
            if (items.length === 0) return null;
            return (
              <div key={s.title}>
                <h6>{s.title}</h6>
                {items.map((i) => (
                  <NavLink key={i.to} to={i.to} end={i.end} className={({ isActive }) => (isActive ? "active" : "")}>
                    <i className={"bi " + i.icon} />{i.label}
                  </NavLink>
                ))}
              </div>
            );
          })}
        </nav>
        <main className="main" key={location.pathname}>
          <Outlet />
          <div className="main-foot">cactusTrade {mode?.version} · software, not financial advice — trading can lose money.</div>
        </main>
      </div>
    </>
  );
}
