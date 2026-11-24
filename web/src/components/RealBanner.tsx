import { useMode, useStatusFailed } from "../lib/mode";

// RealBanner is the red, non-dismissible "REAL MONEY" banner on every signed-in page while
// real trading is armed on any account the user can see (OD27). The list comes from the
// server (status.realArmed); React decides nothing. It is in addition to R3's red frame,
// which follows the current account's mode. When the server cannot read a gate
// (status.realArmedUnknown) or the status itself cannot be read, the banner says the state is
// unknown: a failure is never shown as "not armed".
export function RealBanner() {
  const status = useMode();
  const failed = useStatusFailed();
  const armed = status?.realArmed ?? [];
  const unknown = failed || status?.realArmedUnknown === true;
  if (!unknown && armed.length === 0) return null;
  return (
    <div className="real-banner" role="alert" aria-live="assertive">
      <i className="bi bi-exclamation-octagon-fill me-2" aria-hidden="true" />
      {unknown
        ? <>REAL MONEY — real-trading state unknown: it cannot be read now. Treat real trading as armed until this clears.
          {armed.length > 0 && <> Last known armed on: {armed.map((a) => a.name).join(", ")}</>}</>
        : <>REAL MONEY — real trading armed on: {armed.map((a) => a.name).join(", ")}</>}
    </div>
  );
}
