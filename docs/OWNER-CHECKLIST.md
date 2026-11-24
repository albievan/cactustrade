# Owner checklist — step by step

Address: **https://192.168.1.171:8453** (accept the self-signed certificate warning once).
Everything here is **demo money** unless a step says otherwise. cactusTrade never trades real
money in this build.

---

## 1. Demo trading run (PLAN 4.17) and decision log run (PLAN 5.8)

Do this on a weekday during New York trading hours (**2:30 PM – 9:00 PM UK time**), starting
at least an hour before the close. London (LSE) entries are switched off until this run
confirms eToro's price units, so use New York shares.

**Prepare (5 minutes)**
1. Sign in as **albie** (the owner of *eToto-Demo*, which holds your watchlist).
2. Left menu → **Platform accounts** → on the *eToto-Demo* card press the key icon and check
   the eToro keys say *set*. Press the plug icon (**Test connection**): it must succeed.
3. Header account picker → choose **eToto-Demo**.
4. Left menu → **LLM reviewer**:
   - either switch **Review with an LLM** off (5.8 then logs decisions without a reviewer), or
   - keep it on: make sure the default profile has a key, press its **Test** icon, and enter
     its prices (input/output USD per million tokens) so costs are shown.
5. Left menu → **Risk limits** → in the **Agents** section make sure *eToto-Demo* is
   **Stopped** (press the ■ icon if not), then in the form set:
   - **Trading budget**: a small amount, e.g. **500.00**,
   - **Short selling**: leave **2500** bp (so shorts are allowed),
   - leave the rest as is → **Save limits**.
6. Left menu → **Watchlist** → make sure it has a few liquid New York shares (e.g. AAPL,
   MSFT, NVDA). Search and add if needed (London `.L` shares will not be traded).
7. Left menu → **Dashboard** → the *Agent* panel: if it lists **Before the agent can start**,
   press each button shown (e.g. **Reconcile**) until the list is empty.

**Run (1–3 hours)**
8. Dashboard → Agent panel → press ▶ **Start**. The state turns *Running*.
9. Leave it running. The agent decides at every 5-minute bar close. Watch:
   - **Dashboard**: orders and fills feed, positions (with their stop), distance to limits;
   - **Orders**: each order's state (sent → pending → filled);
   - **Decision log**: one row per idea with the reviewer's verdict, risk result and outcome
     (open a row to see the prompt, response, tokens and cost).
10. You need **at least one long and one short** opened by cactusTrade. The agent chooses the
    direction from the market; if no short has appeared after a couple of hours, carry on
    another day — do not change any rule to force one.
11. **Restart test** (with a position open): left menu → **System** → **Restart** (confirm).
    After it is back (about 30 s), Dashboard → Agent panel shows *reconciled*, the position
    is still listed as cactusTrade's, and the agent is running again.
12. **Kill switch test** (with 2 positions open, or as many as there are): Dashboard → red ⊗
    **Kill switch** → type a reason → confirm. Check: both positions close within a minute
    (Orders page), the agent shows *Killed*.
13. Reset it: Dashboard → **Reset kill switch** (admin) → type the phrase shown (e.g.
    `RESET eToto-Demo`) → confirm. The agent comes back *Stopped*.
14. Before the close: if anything is still open, check it is closed by **10 minutes before
    9:00 PM UK** (the agent does this by itself).

**Report back**
15. Tell me in this chat: "demo run done" plus anything odd you saw (an order stuck *pending*,
    an error banner, a position not closed). I then read the orders, decisions and events from
    the database, settle U1–U11 in `docs/etoro-api.md`, record sanitised fixtures (no keys,
    no account numbers) and mark 4.17 and 5.8 done. The same run lets me finish 2.7 (London
    price units) and 3.6 (the honest backtest on your watchlist).

---

## 2. Phase-6 live dashboard test (35 minutes)

Any time, demo account only (can be during step 1).

1. Sign in, header picker → **eToto-Demo**, left menu → **Dashboard**.
2. Leave it open **35 minutes**. Near the top the badge should stay **live**; around minute 30
   it briefly reconnects and returns to **live** by itself, without a page reload, and the
   figures keep changing. It must not show **stale** while things are normal.
3. Open the Dashboard in a **second browser tab**. In tab 1 press the **Kill switch** and
   confirm. Tab 2 must show *Killed* without a reload. Reset it afterwards (step 1.13).
4. Stream-down test: in tab 1 open the browser's developer tools → **Network** → right-click
   any `/api/v1/live` request → **Block request URL**. After about three retries the badge
   says *stream down · refreshing every 15 s*. The **Kill switch** button must still work.
   Unblock afterwards.
5. Switch to another tab for over a minute, come back: the badge returns to **live**.
6. Left menu → **History** → press each CSV button (**Trades**, **Daily figures**, **Equity
   snapshots**, **Orders**, **Decisions**), open each file in Excel/Numbers: no cell turns into
   a formula, amounts are plain numbers, times end in `Z` (UTC).
7. On **History**, check fees: if eToro reported none they show *unknown*, never $0.00.
8. Tell me "phase 6 test done" and anything that behaved differently.

---

## 3. Release signing key (only if you want installable releases)

On your Mac, in Terminal:

1. `mkdir -p ~/.config/cactustrade`
2. `ssh-keygen -t ed25519 -C cactustrade-release -f ~/.config/cactustrade/release_ed25519`
   (choose a passphrase; keep a copy of both files offline, e.g. on a USB stick in a drawer).
3. `cat ~/.config/cactustrade/release_ed25519.pub` and paste the line into this chat. I put it
   into `scripts/allowed_signers`, `setup.sh` and `setup.ps1` (a test checks all three match).
   The public line is not secret; never paste the file without `.pub`.
4. To build a release later: `cd ~/projects/cactusTrader && make dist` (asks for the
   passphrase), then upload the six files in `dist/` plus `SHA256SUMS` and
   `SHA256SUMS.sig` as a GitHub release `v0.1.0`.

---

## 4. Kubernetes trusted proxies (only if you install on k3s/k8s)

Your current install is Docker Desktop: **nothing to do**. If you later use k3s:

1. `kubectl -n kube-system get pods -o wide | grep -i traefik` → note the pod IP (e.g.
   `10.42.0.7`).
2. In `~/projects/cactusTrader/.env.local` add `K8S_TRUSTED_PROXIES=10.42.0.0/24` (the
   node's pod range; single-node k3s is usually this).
3. `./deploy.sh --upgrade --k3s`.
4. Check: sign in from another computer, left menu → **Audit log**: the sign-in shows that
   computer's address, not a `10.42.x.x` one.

---

## 5. Open decisions (OD14–OD37)

Each has a cautious default that is already in force. To answer:

1. Open `docs/PLAN.md` and search for **OD14**; the list runs to OD37 (OD34–OD37 are at the
   end of Phase 7, "Real-gate review").
2. For each one, reply in this chat with the number and your choice, e.g. "OD18: backfill
   365 days" or "OD19: keep the strict rule". Short answers are fine; "keep all defaults" is
   also an answer.
3. The ones that matter most before real money: **OD19–OD28** (what counts as a demo trading
   day, who may arm, real-money limits, expiry) and **OD34–OD37** (when real trading switches
   itself off).

---

## 6. Real-money preparation (PLAN 7.18) — only when you are ready

Nothing here trades. Real trading stays impossible until you give written consent.

**A. Read-only checks with your real eToro keys (RU1–RU10)**
1. In eToro (real account) → Settings → Trading → **API Key Management** → **Create New
   Key**. If eToro offers permission choices, give it **read only** (no trading).
2. cactusTrade → **Platform accounts** → platform **eToro**, environment **Real**, name e.g.
   *eToro Real* → paste the Public API key and the User key → **Add**.
3. Press the plug icon (**Test connection**) on the new card. cactusTrade only reads from a
   real account; the red real-money frame appears around the page when you pick it.
4. Tell me "real read keys added". I then check RU1–RU10 read-only (prices, eligibility,
   costs, cash rules) and record the answers.

**B. Before consent (all must be true; I will confirm each)**
- Section 1 done (4.17, 5.8), plus 2.7 and 3.6.
- At least **20 qualifying demo trading days** in the last 60 (left menu → **Real trading**
  shows the count and the checklist).
- OD19–OD28 and OD34–OD37 answered.

**C. Consent**
5. Write in this chat, in your own words, that you consent to enabling real trading, the date,
   and the real-money limits you accept (budget, daily loss, drawdown, largest position, open
   positions, shorts). I record it as a decision in the plan.
6. Only then do I build steps 7.19–7.23 (lifting the refusals behind the gate), with reviews.
7. **First real run (7.24) is yours alone**: left menu → **Real trading** → arm the account
   (typed phrase + password), start with the smallest trade you choose, and practise
   **Disarm** and the **Kill switch**. I never place a real order.
