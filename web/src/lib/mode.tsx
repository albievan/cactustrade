import { createContext, useCallback, useContext, useEffect, useState, type ReactNode } from "react";
import { api, type SystemStatus } from "./api";

// The trading mode of the account the user is working on comes from the server and is
// painted on <body data-mode=…> so every page shows demo (teal) or real money (red).
interface ModeValue { status: SystemStatus | null; refresh: () => Promise<void> }
const ModeContext = createContext<ModeValue>({ status: null, refresh: async () => undefined });

export function ModeProvider({ children }: { children: ReactNode }) {
  const [status, setStatus] = useState<SystemStatus | null>(null);
  const refresh = useCallback(async () => {
    try { setStatus(await api.status()); } catch { /* keep the last known */ }
  }, []);
  useEffect(() => {
    void refresh();
    const t = window.setInterval(() => void refresh(), 30_000);
    return () => window.clearInterval(t);
  }, [refresh]);
  useEffect(() => {
    document.body.dataset.mode = status?.mode === "real" ? "real" : "demo";
    return () => { delete document.body.dataset.mode; };
  }, [status?.mode]);
  return <ModeContext.Provider value={{ status, refresh }}>{children}</ModeContext.Provider>;
}

export function useMode(): SystemStatus | null {
  return useContext(ModeContext).status;
}

export function useRefreshMode(): () => Promise<void> {
  return useContext(ModeContext).refresh;
}
