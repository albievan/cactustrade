import { useEffect, useState } from "react";
import { NavLink, Outlet } from "react-router-dom";
import { api, type SystemStatus } from "../lib/api";
import { useSession } from "../lib/session";

export function Layout() {
  const { me, can, signOut } = useSession();
  const [status, setStatus] = useState<SystemStatus | null>(null);
  useEffect(() => {
    api.status().then(setStatus).catch(() => setStatus(null));
  }, []);
  const real = status?.mode === "real";
  const link = ({ isActive }: { isActive: boolean }) => "nav-link" + (isActive ? " active" : "");
  return (
    <>
      {real && <div className="real-banner">REAL MONEY MODE — orders use your live eToro account</div>}
      <nav className="navbar navbar-expand-md border-bottom bg-body-tertiary">
        <div className="container-fluid">
          <span className="navbar-brand">
            <i className="bi bi-flower1 me-1" /> cactusTrade
          </span>
          {status && (
            <span className={"badge me-3 " + (real ? "text-bg-danger" : "demo-badge")} title="Trading mode">
              {status.mode.toUpperCase()}
            </span>
          )}
          <ul className="navbar-nav me-auto">
            <li className="nav-item"><NavLink className={link} to="/" end>Dashboard</NavLink></li>
            <li className="nav-item"><NavLink className={link} to="/credentials">Credentials</NavLink></li>
            {can("users.manage") && <li className="nav-item"><NavLink className={link} to="/users">Users</NavLink></li>}
            <li className="nav-item"><NavLink className={link} to="/api-keys">API keys</NavLink></li>
            {can("audit.view") && <li className="nav-item"><NavLink className={link} to="/audit">Audit</NavLink></li>}
          </ul>
          <ul className="navbar-nav">
            <li className="nav-item"><a className="nav-link" href="/api/docs">API</a></li>
            <li className="nav-item">
              <NavLink className={link} to="/account"><i className="bi bi-person-circle me-1" />{me?.user.username} <span className="text-secondary">({me?.user.role})</span></NavLink>
            </li>
            <li className="nav-item">
              <button className="btn btn-link nav-link" onClick={() => void signOut()}>Sign out</button>
            </li>
          </ul>
        </div>
      </nav>
      <main className="container py-4">
        <Outlet />
      </main>
      <footer className="container pb-4 small text-secondary">
        cactusTrade {status?.version} · software, not financial advice — trading can lose money.
      </footer>
    </>
  );
}
