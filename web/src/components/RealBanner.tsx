import { useMode } from "../lib/mode";

// RealBanner is the red, non-dismissible "REAL MONEY" banner on every signed-in page while
// real trading is armed on any account the user can see (OD27). The list comes from the
// server (status.realArmed); React decides nothing. It is in addition to R3's red frame,
// which follows the current account's mode.
export function RealBanner() {
  const status = useMode();
  const armed = status?.realArmed ?? [];
  if (armed.length === 0) return null;
  return (
    <div className="real-banner" role="alert" aria-live="assertive">
      <i className="bi bi-exclamation-octagon-fill me-2" aria-hidden="true" />
      REAL MONEY — real trading armed on: {armed.map((a) => a.name).join(", ")}
    </div>
  );
}
