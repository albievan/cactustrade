import { createContext, useCallback, useContext, useEffect, useState, type ReactNode } from "react";
import { api, type SystemStatus } from "./api";

// The trading mode of the account the user is working on comes from the server and is
// painted on <body data-mode=…> so every page shows demo (teal) or real money (red).
// failed says that the latest status read failed: the real-trading state is then unknown
// (the banner says so; it never reads a failure as "not armed").
interface ModeValue { status: SystemStatus | null; failed: boolean; refresh: () => Promise<void> }
const ModeContext = createContext<ModeValue>({ status: null, failed: false, refresh: async () => undefined });

export function ModeProvider({ children }: { children: ReactNode }) {
  const [status, setStatus] = useState<SystemStatus | null>(null);
  const [failed, setFailed] = useState(false);
  const refresh = useCallback(async () => {
    try {
      setStatus(await api.status());
      setFailed(false);
    } catch {
      setFailed(true); // keep the last known mode; the banner shows "state unknown"
    }
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
  return <ModeContext.Provider value={{ status, failed, refresh }}>{children}</ModeContext.Provider>;
}

export function useMode(): SystemStatus | null {
  return useContext(ModeContext).status;
}

// useStatusFailed reports whether the latest status read failed.
export function useStatusFailed(): boolean {
  return useContext(ModeContext).failed;
}

export function useRefreshMode(): () => Promise<void> {
  return useContext(ModeContext).refresh;
}
