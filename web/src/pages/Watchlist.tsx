import { useCallback, useEffect, useRef, useState, type ChangeEvent, type FormEvent } from "react";
import {
  api, errorText, usd, WATCHLIST_EXPORT_URL, type Instrument, type Quote, type Theme, type ThemeProposals, type WatchHolding,
  type PlatformWatchlist, type WatchItem, type WatchlistCount, type WatchlistImportResult, type WatchlistOverview,
} from "../lib/api";
import { useSession } from "../lib/session";
import { useMode, useRefreshMode } from "../lib/mode";
import { NeedAccount } from "../components/NeedAccount";
import { ProblemAlert } from "../components/ProblemAlert";
import { ResearchCard } from "../components/ResearchCard";

const QUOTE_REFRESH_MS = 15_000; // 4 calls a minute: well inside eToro's market-data quota
const MAX_FILE_BYTES = 1 << 20; // the server refuses bigger files too (R27)

// EXCHANGES names a market for people (NYSE stands for NYSE and NASDAQ: same hours).
const EXCHANGES: Record<string, string> = { LSE: "LSE", NYSE: "NYSE / NASDAQ", ASX: "ASX" };

// Filter is the theme filter: every share, one theme's (by id), or those without a theme.
type Filter = { kind: "all" } | { kind: "none" } | { kind: "theme"; id: string };

// The instruments the agent may trade. Adding asks eToro whether the share can be bought as a
// real share, long, at leverage 1; anything else is refused with eToro's reason. Themes,
// auto-assign, the JSON file and the holdings (R27) are decided by the server: canEdit comes
// from it, and the page only shows what it answers.
export function Watchlist() {
  const { can } = useSession();
  const status = useMode();
  const access = status?.account?.access;
  const admin = can("agent.control") && (access === "trade" || access === "owner");
  const [items, setItems] = useState<WatchItem[]>([]);
  const [themes, setThemes] = useState<Theme[]>([]);
  const [overview, setOverview] = useState<WatchlistOverview | null>(null);
  const [quotes, setQuotes] = useState<Record<number, Quote>>({});
  const [filter, setFilter] = useState<Filter>({ kind: "all" });
  const [proposals, setProposals] = useState<ThemeProposals | null>(null);
  const [imported, setImported] = useState<WatchlistImportResult | null>(null);
  const [platformLists, setPlatformLists] = useState<PlatformWatchlist[] | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [loaded, setLoaded] = useState(false);
  const load = useCallback(() => {
    api.watchlist().then((ws) => { setItems(ws); setLoaded(true); }).catch(setError);
    api.themes().then(setThemes).catch(() => undefined);
    api.watchlistOverview().then(setOverview).catch(() => undefined);
  }, []);
  const accountId = status?.account?.id;
  useEffect(() => { if (accountId) load(); }, [load, accountId]);
  useEffect(() => {
    if (items.length === 0) return;
    const tick = () =>
      api.watchlistQuotes()
        .then((qs) => setQuotes(Object.fromEntries(qs.map((q) => [q.instrumentId, q]))))
        .catch(() => undefined);
    tick();
    const t = window.setInterval(tick, QUOTE_REFRESH_MS);
    return () => window.clearInterval(t);
  }, [items]);
  const canEdit = overview?.canEdit === true;
  async function run(f: () => Promise<void>) {
    setBusy(true);
    setError("");
    try { await f(); } catch (e) { setError(e); } finally { setBusy(false); }
  }
  async function remove(id: number) {
    await run(async () => { await api.removeFromWatchlist(id); load(); });
  }
  async function assign(pairs: { instrumentId: number; themeId: string }[]) {
    await run(async () => { await api.assignThemes(pairs); load(); });
  }
  if (status && !status.account) return <><h1 className="h3">Watchlist</h1><NeedAccount /></>;
  const themeName = (id?: string) => themes.find((t) => t.id === id)?.name;
  const shown = items.filter((w) => {
    switch (filter.kind) {
      case "all": return true;
      case "none": return !w.themeId;
      default: return w.themeId === filter.id;
    }
  });
  return (
    <>
      <h1 className="h3">Watchlist</h1>
      <p className="text-secondary">
        Shares the agent may trade: London (LSE), New York (NYSE / NASDAQ) and Sydney (ASX; search e.g. “CBA.ASX”) only, bought as the real share at leverage 1.
      </p>
      <ProblemAlert error={error} />
      {admin && <AddInstrument onAdded={load} />}
      {accountId && <ResearchCard accountId={accountId} onChanged={load} />}
      <div className="d-flex flex-wrap align-items-center gap-2 mb-3">
        {items.length > 0 && <ThemeFilter themes={themes} filter={filter} onChange={setFilter} />}
        {canEdit && (
          <div className="btn-group btn-group-sm ms-auto" role="group" aria-label="Watchlist tools">
            <button type="button" className="btn btn-outline-secondary" title="Auto-assign themes" aria-label="Auto-assign themes"
              disabled={busy || items.length === 0} onClick={() => void run(async () => { setProposals(await api.themeProposals()); })}>
              <i className="bi bi-magic" />
            </button>
            <a className="btn btn-outline-secondary" href={WATCHLIST_EXPORT_URL} download
              title="Export the watchlist and themes as JSON" aria-label="Export the watchlist and themes as JSON">
              <i className="bi bi-download" />
            </a>
            <button type="button" className="btn btn-outline-secondary" title="Import a watchlist from eToro"
              aria-label="Import a watchlist from eToro" disabled={busy}
              onClick={() => void run(async () => { setImported(null); setPlatformLists(await api.platformWatchlists()); })}>
              <i className="bi bi-cloud-download" />
            </button>
            <ImportButton disabled={busy} onPicked={(file) => void run(async () => {
              setImported(null);
              setImported(await api.importWatchlist(file));
              load();
            })} onError={setError} />
          </div>
        )}
      </div>
      {platformLists && (
        <PlatformImport lists={platformLists} busy={busy} onClose={() => setPlatformLists(null)}
          onImport={(id) => void run(async () => {
            setImported(await api.importPlatformWatchlist(id));
            setPlatformLists(null);
            load();
          })} />
      )}
      {imported && <ImportResult result={imported} onClose={() => setImported(null)} />}
      {proposals && (
        <Proposals proposals={proposals} busy={busy} onClose={() => setProposals(null)}
          onAccept={async (ps) => {
            await assign(ps.map((p) => ({ instrumentId: p.instrumentId, themeId: p.themeId })));
            const left = proposals.proposals.filter((p) => !ps.includes(p));
            setProposals(left.length > 0 || proposals.unmatched.length > 0 ? { ...proposals, proposals: left } : null);
          }} />
      )}
      {overview && items.length > 0 && <HoldingsSummary overview={overview} />}
      {items.length === 0 ? (
        loaded && status?.account && <EmptyWatchlist accountName={status.account.name} />
      ) : (
        <div className="table-responsive">
          <table className="table align-middle">
            <thead><tr>
              <th>Symbol</th><th>Name</th><th>Exchange</th><th className="text-end">Bid</th><th className="text-end">Ask</th>
              <th>Eligibility</th><th>Theme</th><th>Holding</th>{admin && <th />}
            </tr></thead>
            <tbody>
              {shown.map((w) => {
                const q = quotes[w.instrument.instrumentId];
                return (
                  <tr key={w.instrument.instrumentId}>
                    <td><span className="fw-semibold">{w.instrument.symbol}</span> <OriginBadge item={w} /></td>
                    <td>{w.instrument.name}</td>
                    <td><ExchangeBadge market={w.instrument.market} /></td>
                    <td className="text-end">{q?.bid ?? "—"}</td>
                    <td className="text-end">{q?.ask ?? "—"}{q?.delayed && <span className="badge text-bg-secondary ms-1">delayed</span>}</td>
                    <td className="small">
                      {w.eligibility.realLong ? <span className="text-success"><i className="bi bi-check-circle me-1" />real share x1</span> : <span className="text-danger">{w.eligibility.reason}</span>}
                      {w.eligibility.checkedAt && <div className="text-secondary">checked {new Date(w.eligibility.checkedAt).toLocaleDateString()}</div>}
                    </td>
                    <td>
                      {canEdit ? (
                        <select className="form-select form-select-sm" aria-label={`Theme for ${w.instrument.symbol}`} value={w.themeId ?? ""} disabled={busy}
                          onChange={(e) => void assign([{ instrumentId: w.instrument.instrumentId, themeId: e.target.value }])}>
                          <option value="">— none —</option>
                          {themes.map((t) => <option key={t.id} value={t.id}>{t.name}</option>)}
                        </select>
                      ) : (
                        <span className={w.themeId ? "" : "text-secondary"}>{themeName(w.themeId) ?? "—"}</span>
                      )}
                    </td>
                    <td className="small"><HoldingCell holding={w.holding} /></td>
                    {admin && <td className="text-end">
                      <button className="btn btn-sm btn-outline-danger" title={`Remove ${w.instrument.symbol}`} aria-label={`Remove ${w.instrument.symbol}`}
                        onClick={() => void remove(w.instrument.instrumentId)}><i className="bi bi-trash" /></button>
                    </td>}
                  </tr>
                );
              })}
            </tbody>
          </table>
          {shown.length === 0 && <p className="text-secondary">No share matches this filter.</p>}
        </div>
      )}
    </>
  );
}

// ThemeFilter: compact toggle chips "All", one per theme, "No theme".
function ThemeFilter({ themes, filter, onChange }: { themes: Theme[]; filter: Filter; onChange: (f: Filter) => void }) {
  const chip = (label: string, f: Filter, pressed: boolean) => (
    <button key={label + (f.kind === "theme" ? f.id : "")} type="button" aria-pressed={pressed} title={`Show ${label === "All" ? "every share" : label}`}
      className={"btn btn-sm rounded-pill " + (pressed ? "btn-primary" : "btn-outline-secondary")} onClick={() => onChange(f)}>
      {label}
    </button>
  );
  return (
    <div className="d-flex flex-wrap gap-1" role="group" aria-label="Filter by theme">
      {chip("All", { kind: "all" }, filter.kind === "all")}
      {themes.map((t) => chip(t.name, { kind: "theme", id: t.id }, filter.kind === "theme" && filter.id === t.id))}
      {chip("No theme", { kind: "none" }, filter.kind === "none")}
    </div>
  );
}

// ORIGINS says who put a share on the watchlist, from the server's origin (R29).
const ORIGINS: Record<WatchItem["origin"], { label: string; cls: string }> = {
  user: { label: "you", cls: "text-bg-light border" },
  scanner: { label: "scanner", cls: "text-bg-secondary" },
  research: { label: "research", cls: "text-bg-info" },
};

function OriginBadge({ item }: { item: WatchItem }) {
  const o = ORIGINS[item.origin] ?? ORIGINS.user;
  const who = item.origin === "user" ? item.addedBy : item.origin === "research" ? "web research" : "the scanner";
  return <span className={`badge ${o.cls}`} title={`Added by ${who} on ${new Date(item.addedAt).toLocaleDateString()}`}>{o.label}</span>;
}

function ExchangeBadge({ market }: { market?: string }) {
  if (!market) return <span className="text-secondary">—</span>;
  return <span className="badge text-bg-light border" title={`Listed on ${EXCHANGES[market] ?? market}`}>{EXCHANGES[market] ?? market}</span>;
}

// whoOpened says who opened a holding's positions, as the server counted them.
function whoOpened(h: WatchHolding): string {
  if (h.ours === 0) return "yours";
  return h.ours === h.positions ? "cactusTrade" : "mixed";
}

function HoldingCell({ holding: h }: { holding?: WatchHolding }) {
  if (!h) return <span className="text-secondary">—</span>;
  return (
    <div aria-label={`Holding of ${h.symbol}`}>
      <div>{h.units} sh · {usd(h.valueCents)}</div>
      <div className={h.pnlCents < 0 ? "text-danger" : h.pnlCents > 0 ? "text-success" : "text-secondary"}>P&amp;L {usd(h.pnlCents)}</div>
      <span className={"badge " + (h.ours > 0 ? "text-bg-info" : "text-bg-secondary")}
        title={`${h.positions} position(s), ${h.ours} opened by cactusTrade`}>{whoOpened(h)}</span>
      {h.shorts > 0 && <span className="badge text-bg-warning ms-1">short</span>}
    </div>
  );
}

// HoldingsSummary: what the account holds on watchlist shares, and what it holds elsewhere.
function HoldingsSummary({ overview }: { overview: WatchlistOverview }) {
  const h = overview.holdings;
  return (
    <div className="card mb-3" aria-label="Holdings">
      <div className="card-body py-2">
        <div className="d-flex flex-wrap align-items-center gap-2">
          <span className="fw-semibold">Holdings</span>
          {h.known ? (
            <>
              <span>{usd(h.watchlistValueCents)} on watchlist shares ({h.watchlistPositions} position{h.watchlistPositions === 1 ? "" : "s"}, P&amp;L {usd(h.watchlistPnlCents)})</span>
              {h.stale && <span className="badge text-bg-warning" title={h.asOf ? `read ${new Date(h.asOf).toLocaleTimeString()}` : undefined}>stale</span>}
            </>
          ) : (
            <span className="text-secondary">not known: {h.error ?? "no portfolio read yet"}</span>
          )}
        </div>
        {h.known && h.notOnWatchlist.length > 0 && (
          <div className="small mt-1" aria-label="Held but not on the watchlist">
            <span className="text-secondary me-1">Held, not on the watchlist:</span>
            {h.notOnWatchlist.map((x) => (
              <span key={x.instrumentId} className="ticker-chip me-1" title={`${x.name || x.symbol} · ${x.positions} position(s), ${whoOpened(x)}`}>
                <strong>{x.symbol}</strong> {x.units} sh · {usd(x.valueCents)}
                {x.market && <span className="opacity-75"> · {EXCHANGES[x.market] ?? x.market}</span>}
              </span>
            ))}
          </div>
        )}
      </div>
    </div>
  );
}

// readText reads a picked file as text (FileReader: every browser, and the test DOM).
function readText(f: Blob): Promise<string> {
  return new Promise((resolve, reject) => {
    const r = new FileReader();
    r.onload = () => resolve(String(r.result));
    r.onerror = () => reject(r.error ?? new Error("read failed"));
    r.readAsText(f);
  });
}

// ImportButton opens a JSON file and hands its parsed content on. The server checks it.
function ImportButton({ disabled, onPicked, onError }: { disabled: boolean; onPicked: (file: unknown) => void; onError: (e: unknown) => void }) {
  const input = useRef<HTMLInputElement>(null);
  async function picked(e: ChangeEvent<HTMLInputElement>) {
    const f = e.target.files?.[0];
    e.target.value = "";
    if (!f) return;
    if (f.size > MAX_FILE_BYTES) { onError("The file is larger than 1 MiB."); return; }
    let text: string;
    try {
      text = await readText(f);
    } catch {
      onError("The file could not be read.");
      return;
    }
    let parsed: unknown;
    try {
      parsed = JSON.parse(text);
    } catch {
      onError("The file is not JSON.");
      return;
    }
    onPicked(parsed);
  }
  return (
    <>
      <button type="button" className="btn btn-outline-secondary" title="Import a watchlist file (JSON)" aria-label="Import a watchlist file (JSON)"
        disabled={disabled} onClick={() => input.current?.click()}>
        <i className="bi bi-upload" />
      </button>
      <input ref={input} type="file" accept=".json,application/json" className="d-none" aria-label="Watchlist file" onChange={(e) => void picked(e)} />
    </>
  );
}

// PlatformImport lets the user pick one of their eToro watchlists to import (R46, D38). The
// server reads eToro (never writes) and checks every share like a hand add.
function PlatformImport({ lists, busy, onImport, onClose }: {
  lists: PlatformWatchlist[]; busy: boolean; onImport: (id: string) => void; onClose: () => void;
}) {
  const [id, setId] = useState(() => (lists.find((l) => l.isDefault) ?? lists[0])?.id ?? "");
  const picked = lists.find((l) => l.id === id);
  return (
    <div className="card mb-3" aria-label="Import from eToro"><div className="card-body py-2">
      <div className="d-flex align-items-center mb-2">
        <strong className="me-auto">Import a watchlist from eToro</strong>
        <button type="button" className="btn-close" aria-label="Close" onClick={onClose} />
      </div>
      {lists.length === 0 ? (
        <p className="small text-secondary mb-0">This account has no watchlists on eToro.</p>
      ) : (
        <>
          <div className="input-group input-group-sm" style={{ maxWidth: 480 }}>
            <select className="form-select" aria-label="eToro watchlist" value={id} onChange={(e) => setId(e.target.value)}>
              {lists.map((l) => <option key={l.id} value={l.id}>{l.name} ({l.shares} {l.shares === 1 ? "share" : "shares"})</option>)}
            </select>
            <button type="button" className="btn btn-primary" disabled={busy || !picked || picked.shares === 0} onClick={() => onImport(id)}>Import</button>
          </div>
          <div className="form-text">
            Each share is checked with eToro like adding by hand (exchange, eligibility); refused ones are listed, not added.
            {picked && picked.others > 0 && ` ${picked.others} people on this list are left out.`} Nothing changes on eToro.
          </div>
        </>
      )}
    </div></div>
  );
}

// ImportResult shows what the server merged, in the style of a ProblemAlert.
function ImportResult({ result: r, onClose }: { result: WatchlistImportResult; onClose: () => void }) {
  const problems = r.skipped.length + r.themesSkipped.length;
  const line = (label: string, xs: string[]) => xs.length > 0 && <li>{label}: {xs.join(", ")}</li>;
  return (
    <div className={`alert alert-${problems > 0 ? "warning" : "success"} d-flex gap-2`} role="status" aria-label="Import result">
      <div className="flex-grow-1">
        <strong>Imported:</strong> {r.added.length} share{r.added.length === 1 ? "" : "s"} added, {r.already.length} already there,{" "}
        {r.skipped.length} refused; themes {r.themesCreated.length} created, {r.themesUpdated.length} updated, {r.themesUnchanged.length} unchanged.
        <ul className="mb-0 small">
          {line("Added", r.added)}
          {line("Themes created", r.themesCreated)}
          {line("Themes updated", r.themesUpdated)}
          {r.skipped.map((s) => <li key={"s" + s.name + (s.instrumentId ?? "")}>Not added: <strong>{s.name}</strong> — {s.reason}</li>)}
          {r.themesSkipped.map((s) => <li key={"t" + s.name}>Theme not imported: <strong>{s.name}</strong> — {s.reason}</li>)}
          {r.notes.map((n) => <li key={"n" + n}>{n}</li>)}
        </ul>
      </div>
      <button type="button" className="btn-close" aria-label="Close the import result" onClick={onClose} />
    </div>
  );
}

// Proposals lists auto-assign's suggestions; nothing changes until one is accepted.
function Proposals({ proposals: ps, busy, onAccept, onClose }: {
  proposals: ThemeProposals; busy: boolean;
  onAccept: (ps: ThemeProposals["proposals"]) => Promise<void>; onClose: () => void;
}) {
  return (
    <div className="card mb-3" aria-label="Proposed themes">
      <div className="card-body">
        <div className="d-flex align-items-center gap-2 mb-2">
          <h2 className="h6 mb-0 me-auto">Proposed themes</h2>
          {ps.proposals.length > 0 && (
            <button type="button" className="btn btn-sm btn-success" disabled={busy} onClick={() => void onAccept(ps.proposals)}
              title="Accept every proposal" aria-label="Accept every proposal"><i className="bi bi-check2-all me-1" />Accept all</button>
          )}
          <button type="button" className="btn-close" aria-label="Close the proposals" onClick={onClose} />
        </div>
        {ps.proposals.length === 0 && <p className="text-secondary small mb-2">No share without a theme matches a theme.</p>}
        <ul className="list-group">
          {ps.proposals.map((p) => (
            <li key={p.instrumentId} className="list-group-item d-flex align-items-center gap-2">
              <button type="button" className="btn btn-sm btn-outline-success" disabled={busy} onClick={() => void onAccept([p])}
                title={`Put ${p.symbol} into ${p.themeName}`} aria-label={`Accept ${p.symbol} → ${p.themeName}`}><i className="bi bi-check-lg" /></button>
              <span className="fw-semibold">{p.symbol}</span><i className="bi bi-arrow-right" aria-hidden="true" /><span>{p.themeName}</span>
              <span className="small text-secondary ms-auto">score {p.score}: {p.reasons.join("; ")}</span>
            </li>
          ))}
        </ul>
        {ps.unmatched.length > 0 && <p className="small text-secondary mt-2 mb-0">No theme fits: {ps.unmatched.join(", ")}.</p>}
        {ps.notes.map((n) => <p key={n} className="small text-secondary mt-1 mb-0">{n}</p>)}
      </div>
    </div>
  );
}

// EmptyWatchlist says whose watchlist is empty (R25: a watchlist belongs to one eToro account)
// and names the caller's other accounts that do have shares, from the server's counts, with a
// button that switches to that account. Accounts the caller cannot see are never named.
function EmptyWatchlist({ accountName }: { accountName: string }) {
  const refreshMode = useRefreshMode();
  const [others, setOthers] = useState<WatchlistCount[]>([]);
  useEffect(() => {
    api.watchlistCounts().then((cs) => setOthers(cs.filter((c) => !c.current && c.count > 0))).catch(() => undefined);
  }, [accountName]);
  async function pick(id: string) {
    await api.setCurrentAccount(id);
    await refreshMode(); // the page reloads the new account's watchlist
  }
  return (
    <div className="card mb-3" aria-label="Empty watchlist">
      <div className="card-body">
        <p className="mb-2"><strong>{accountName}</strong> has no shares on its watchlist.</p>
        {others.length > 0 && (
          <p className="mb-2">
            Shares are on the watchlist of{" "}
            {others.map((c, i) => (
              <span key={c.account.id}>
                {i > 0 && ", "}
                <button type="button" className="btn btn-link p-0 align-baseline" title={`Switch to ${c.account.name}`} onClick={() => void pick(c.account.id)}>
                  {c.account.name}
                </button>{" "}
                ({c.count} {c.count === 1 ? "share" : "shares"})
              </span>
            ))}.
          </p>
        )}
        <p className="small text-secondary mb-0">
          Each eToro account has its own watchlist. An account another user owns appears here only after its owner shares it with you.
        </p>
      </div>
    </div>
  );
}

function AddInstrument({ onAdded }: { onAdded: () => void }) {
  const [q, setQ] = useState("");
  const [results, setResults] = useState<Instrument[]>([]);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  async function search(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      setResults(await api.searchInstruments(q));
    } catch (err) {
      setError(errorText(err));
    } finally {
      setBusy(false);
    }
  }
  async function add(id: number) {
    setError("");
    try {
      await api.addToWatchlist(id);
      setResults([]);
      setQ("");
      onAdded();
    } catch (err) {
      setError(errorText(err));
    }
  }
  return (
    <div className="card mb-4">
      <div className="card-body">
        <form className="d-flex gap-2" onSubmit={search}>
          <input className="form-control" placeholder="Search eToro by name or symbol (e.g. Apple, BARC.L)" aria-label="Search instruments" value={q} maxLength={100} onChange={(e) => setQ(e.target.value)} required />
          <button className="btn btn-outline-primary" disabled={busy}><i className="bi bi-search me-1" />Search</button>
        </form>
        {error && <div className="alert alert-danger mt-3 mb-0" role="alert">{error}</div>}
        {results.length > 0 && (
          <ul className="list-group mt-3">
            {results.map((r) => (
              <li className="list-group-item d-flex align-items-center gap-2" key={r.instrumentId}>
                <span className="fw-semibold">{r.symbol}</span>
                <span className="me-auto text-secondary">{r.name} · {r.type}</span>
                {r.market ? (
                  <button className="btn btn-sm btn-success" onClick={() => void add(r.instrumentId)}>Add ({r.market})</button>
                ) : (
                  <span className="small text-secondary">not tradable here</span>
                )}
              </li>
            ))}
          </ul>
        )}
      </div>
    </div>
  );
}
