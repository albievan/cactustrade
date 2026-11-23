import { createContext, useContext, useEffect, useState, type ReactNode } from "react";
import { api, type SystemStatus } from "./api";

// The trading mode comes from the server and is painted on <body data-mode=…> so every page
// shows demo (teal) or real money (red) without any page having to remember to.
const ModeContext = createContext<SystemStatus | null>(null);

export function ModeProvider({ children }: { children: ReactNode }) {
  const [status, setStatus] = useState<SystemStatus | null>(null);
  useEffect(() => {
    let alive = true;
    const load = () => api.status().then((s) => alive && setStatus(s)).catch(() => undefined);
    void load();
    const t = window.setInterval(load, 30_000);
    return () => { alive = false; window.clearInterval(t); };
  }, []);
  useEffect(() => {
    document.body.dataset.mode = status?.mode === "real" ? "real" : "demo";
    return () => { delete document.body.dataset.mode; };
  }, [status?.mode]);
  return <ModeContext.Provider value={status}>{children}</ModeContext.Provider>;
}

export function useMode(): SystemStatus | null {
  return useContext(ModeContext);
}
