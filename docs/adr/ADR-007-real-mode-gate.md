# ADR-007: The real-mode gate

**Status:** accepted as machinery (2026-11-24, PLAN 7.10–7.17); **lifted 2026-11-27** (PLAN
7.19–7.23) after the owner's written consent, D39: "I give written consent to enable real-money
trading on Real Money Maker. I accept the OD19–OD28 defaults and the real-money limit ceilings
as they are in the plan. I accept the open 4.17 items. I understand losses are real." See
*Lift* at the end; the text below is the machinery as decided on 2026-11-24.

**Context.** cactusTrade trades demo accounts only. Every real-money refusal site (PLAN
S1–S11) refuses whatever else is true. Before any of them can be lifted (7.19–7.23), the
conditions under which real money may be used have to exist, be visible and be tested,
without being usable.

**Decision.**
- **Two keys, plus consent.** Real trading on an account will need (1) an *armed gate* for
  that account, stored in `real_gates` (migration 00012), and (2) the deployment opt-in
  `CACTUSTRADE_REAL_TRADING=on` (OD28, wired in 7.23), and (3) a build with
  `RealTradingEnabled = true`, which only the owner's written consent produces. This build has
  (1) and refuses it; (2) does not exist yet; (3) is false and guarded by
  `TestRealTradingDisabledInThisBuild`.
- **Arming** (`POST /api/v1/accounts/{id}/real-gate/arm`, OD22): the `mode.switch` permission
  (admin), a web session (never an API key), the account's owner, the password re-entered
  (argon2 through the bounded hasher, counted by the sign-in limiter), the phrase
  `ENABLE REAL TRADING <account name>`, then the checklist `domain.GateChecks`: an eToro real
  account with keys, reconciled, not killed, agent stopped, limits within the real ceilings
  (OD25); a linked demo account of the same owner (OD21); the demo evidence (OD19, OD20); the
  arming user. `domain.CanArm` refuses with the failing checks (409) or, with all passing,
  `ErrRealTradingDisabled` (403 "Real trading is not enabled in this build"). Every refusal
  after the password is audited `real.arm_refused`. Nothing is ever stored as armed.
- **Evidence rule** (OD19 strict, OD20): a qualifying demo day is a completed risk day
  (22:00 UTC boundary) of the linked demo account with at least one decision, at least one
  open order in demo mode that was sent and filled (or partly), at least one position opened,
  and every position opened that day closed within it. 20 such days within the last 60
  completed risk days, the latest at most 7 days old. Today's risk day never counts.
- **Real limits** (OD25): defaults budget 0, daily loss 100 bp, drawdown 500 bp, position 1000
  bp, 4 positions, shorts 0 %; ceilings 200 / 1000 / 2000 bp, 8 positions, shorts 25 % (D19),
  a position cap required; an optional deployment cap on the budget (`WithBudgetCap`).
- **Disarming** (`POST …/real-gate/disarm`, OD23): trade access and `agent.kill`, a web
  session or an API key, idempotent, never rate limited (D32). The agent goes to stopped; no
  order is cancelled and no position closed (exits continue). Audited `real.disarm`, a danger
  event, an e-mail to every admin and the owner.
- **Automatic disarm** (OD24): a kill-switch trip, a change of the account's eToro keys, any
  raised limit (`domain.RaisesAny`), a restore after the arming (`restore.at`), and the 30-day
  expiry. The last two are also checked on every read (an expired gate is never in force) and
  persisted by the supervisor loop's sweep. A disarm is latched in memory before it is stored,
  so a failed write still takes the gate out of force in that process.
- **Banner** (OD27): `SystemStatus.realArmed` lists the armed accounts the caller can see; the
  web app shows a red, non-dismissible banner on every signed-in page, in addition to R3's
  red frame.

**Consequences.** The proof that nothing is lifted is the refusal matrix (PLAN 7.17): with a
gate armed through `armLocked`, a reconciled real account with keys, valid limits, its agent
forced to running and the opt-in variable set, every site S1–S11 still refuses and no request
reaches eToro; `TestRealRefusalModeReferences` lists every `ModeReal`/`ModeDemo` reference in
non-test code and fails on a new one until it is reviewed. Lifting a refusal (7.19+) is a
reviewed change of that list, after 7.18.

**Lift (2026-11-27, PLAN 7.19–7.23, D39).** `RealTradingEnabled = true`
(`TestRealTradingEnabledByOwnerConsent`); `CanArm` arms when every check passes. A real entry
needs (1) the gate armed and in force, (2) `CACTUSTRADE_REAL_TRADING=on` (exactly "on"; off by
default, `.env` off, `deploy.sh` never sets it, a warning and a danger event at every start
while on), (3) reconciled, limits within `RealCeilings`, the agent started by hand (OD26: a real
agent never resumes by itself), and (4) a `domain.RealPermit` from `PermitEntry` taken for that
entry (60 s), checked by `AssessEntry`, `IntentFor`, the executor and the eToro client. Exits of
what cactusTrade opened carry `PermitExit` and work whatever the gate and the opt-in. Without a
permit the client sends nothing (`TestRealRefusalEtoroClient`, `TestRealMutationsRefusedWithoutPermit`);
the refusal matrix now proves every site refuses whenever one key is missing
(`TestRealRefusalDomain`, `TestRealRefusalApp`, `TestRealRefusalHTTP`), and
`TestAgentSequencePropertiesReal` drives an armed real account through random disarms, kills,
opt-in switches, expiries and restores. `set_sl_tp` stays not executable. Claude never places a
real-money order; the first real run is the owner's (7.24).

**Data reset (2026-11-28, PLAN R49).** Resetting all trading data deletes the decisions,
orders and agent positions the demo evidence is read from, so it restarts the evidence: every
real-money account needs N new qualifying demo days (default 5, D37) before it can be armed
again. The reset is refused while any gate is armed, locks a gate stored armed
(`DisarmDataReset`), latches the disarm in memory, and stores `data_reset.at`, which counts
like `restore.at` (a gate armed before it is never in force). The kill switch and the
high-water mark (`risk_state`) are kept.
