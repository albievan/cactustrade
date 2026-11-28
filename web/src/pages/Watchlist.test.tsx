import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { SessionProvider } from "../lib/session";
import { ModeProvider } from "../lib/mode";
import { Watchlist } from "./Watchlist";

const me = {
  user: { id: "1", username: "admin", role: "admin", disabled: false, mustChangePassword: false, createdAt: "", updatedAt: "" },
  permissions: ["view", "agent.control", "settings.write"], via: "session", csrfToken: "c",
};

describe("Watchlist", () => {
  it("lists items with live quotes and eligibility", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
      const url = String(input);
      const json = (v: unknown) => new Response(JSON.stringify(v), { status: 200 });
      if (url.endsWith("/api/v1/auth/me")) return json(me);
      if (url.endsWith("/api/v1/system/status")) return json({ version: "0.1.0", build: "1", mode: "demo", agentState: "stopped", database: "sqlite", startedAt: "2026-11-23T09:00:00Z", now: "2026-11-23T09:00:00Z",
        account: { id: "a1", name: "Main", ownerId: "1", ownerName: "admin", mode: "demo", envKeys: false, access: "owner", createdAt: "2026-11-23T09:00:00Z" } });
      if (url.endsWith("/api/v1/watchlist")) return json([{
        instrument: { instrumentId: 1001, symbol: "EXMP", name: "Example Corp", type: "Stocks", market: "NYSE" },
        eligibility: { realLong: true, checkedAt: "2026-11-23T09:00:00Z" }, addedBy: "admin", addedAt: "2026-11-23T09:00:00Z",
      }]);
      if (url.endsWith("/api/v1/watchlist/quotes")) return json([{ instrumentId: 1001, bid: "189.1", ask: "189.2", at: "2026-11-23T09:00:00Z", delayed: true }]);
      return new Response("{}", { status: 404 });
    });
    render(<SessionProvider><ModeProvider><Watchlist /></ModeProvider></SessionProvider>);
    expect(await screen.findByText("EXMP")).toBeInTheDocument();
    expect(await screen.findByText("189.1")).toBeInTheDocument();
    expect(screen.getByText("delayed")).toBeInTheDocument();
    expect(screen.getByText(/real share x1/)).toBeInTheDocument();
    expect(await screen.findByLabelText("Search instruments")).toBeInTheDocument();
  });
});

describe("Watchlist empty (R25)", () => {
  const acct = (id: string, name: string, ownerName = "admin") =>
    ({ id, name, ownerId: "1", ownerName, mode: "demo", platform: "etoro", envKeys: false, access: "owner", createdAt: "2026-11-23T09:00:00Z" });
  // serve has two accounts: a1 (current, empty) and a2; switching changes the current one and
  // its watchlist.
  function serve(counts: { account: ReturnType<typeof acct>; count: number }[]) {
    let current = "a1";
    const puts: unknown[] = [];
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
      const url = String(input);
      const json = (v: unknown) => new Response(JSON.stringify(v), { status: 200 });
      if (url.endsWith("/api/v1/auth/me")) return json(me);
      if (url.endsWith("/api/v1/system/status")) {
        return json({ version: "0.1.0", build: "1", mode: "demo", agentState: "stopped", database: "sqlite", startedAt: "", now: "",
          account: counts.find((c) => c.account.id === current)?.account });
      }
      if (url.endsWith("/api/v1/watchlist/counts")) return json(counts.map((c) => ({ ...c, current: c.account.id === current })));
      if (url.endsWith("/api/v1/watchlist")) {
        return json(current === "a1" ? [] : [{
          instrument: { instrumentId: 1001, symbol: "EXMP", name: "Example Corp", type: "Stocks", market: "NYSE" },
          eligibility: { realLong: true }, addedBy: "albie", addedAt: "2026-11-23T09:00:00Z",
        }]);
      }
      if (url.endsWith("/api/v1/accounts/current") && init?.method === "PUT") {
        const body = JSON.parse(String(init.body));
        puts.push(body);
        current = body.accountId;
        return json(counts.find((c) => c.account.id === current)?.account);
      }
      if (url.endsWith("/api/v1/watchlist/quotes")) return json([]);
      return new Response("{}", { status: 404 });
    });
    render(<SessionProvider><ModeProvider><Watchlist /></ModeProvider></SessionProvider>);
    return puts;
  }

  afterEach(() => vi.restoreAllMocks());

  it("says whose watchlist is empty and links the accounts that have shares", async () => {
    const puts = serve([{ account: acct("a1", "Admin demo"), count: 0 }, { account: acct("a2", "Albie demo", "albie"), count: 3 }]);
    const empty = await screen.findByLabelText("Empty watchlist");
    expect(empty).toHaveTextContent("Admin demo has no shares on its watchlist.");
    expect(await screen.findByText(/Shares are on the watchlist of/)).toHaveTextContent("Albie demo (3 shares)");
    expect(empty).toHaveTextContent("Each eToro account has its own watchlist");
    fireEvent.click(screen.getByRole("button", { name: "Albie demo" }));
    expect(await screen.findByText("EXMP")).toBeInTheDocument();
    expect(puts).toEqual([{ accountId: "a2" }]);
  });

  it("names no other account when none has shares", async () => {
    serve([{ account: acct("a1", "Admin demo"), count: 0 }, { account: acct("a2", "Other"), count: 0 }]);
    expect(await screen.findByLabelText("Empty watchlist")).toHaveTextContent("Admin demo has no shares on its watchlist.");
    await waitFor(() => expect(screen.queryByText(/Shares are on the watchlist of/)).not.toBeInTheDocument());
  });
});

describe("Watchlist themes, file and holdings (R27)", () => {
  const status = (access: string) => ({ version: "0.1.0", build: "1", mode: "demo", agentState: "stopped", database: "sqlite", startedAt: "", now: "",
    account: { id: "a1", name: "Main", ownerId: "1", ownerName: "admin", mode: "demo", envKeys: false, access, createdAt: "2026-11-23T09:00:00Z" } });
  const items = [
    {
      instrument: { instrumentId: 1001, symbol: "EXMP", name: "Example Corp", type: "Stocks", market: "NYSE" },
      eligibility: { realLong: true }, addedBy: "admin", addedAt: "2026-11-23T09:00:00Z", themeId: "t1",
      holding: { instrumentId: 1001, symbol: "EXMP", name: "Example Corp", market: "NYSE", positions: 1, units: "2", valueCents: 40000, costCents: 39000, pnlCents: 1000, ours: 1, shorts: 0 },
    },
    {
      instrument: { instrumentId: 1002, symbol: "BARC.L", name: "Barclays", type: "Stocks", market: "LSE" },
      eligibility: { realLong: true }, addedBy: "admin", addedAt: "2026-11-23T09:00:00Z",
    },
  ];
  const themes = [{ id: "t1", name: "Tech", keywords: ["tech"], industryId: 0, enabled: true, instruments: [1001], shares: [], createdAt: "", updatedAt: "" },
    { id: "t2", name: "Banks", keywords: ["bank"], industryId: 0, enabled: true, instruments: [], shares: [], createdAt: "", updatedAt: "" }];
  function serve(canEdit: boolean) {
    const posts: { url: string; body: unknown }[] = [];
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
      const url = String(input);
      const json = (v: unknown) => new Response(JSON.stringify(v), { status: 200 });
      if (init?.method === "POST") posts.push({ url, body: init.body ? JSON.parse(String(init.body)) : null });
      if (url.endsWith("/api/v1/auth/me")) return json(me);
      if (url.endsWith("/api/v1/system/status")) return json(status(canEdit ? "owner" : "view"));
      if (url.endsWith("/api/v1/watchlist/platform")) return json([
        { id: "w1", name: "Tech", type: "Static", isDefault: false, shares: 3, others: 0 },
        { id: "w2", name: "My Watchlist", type: "Default", isDefault: true, shares: 2, others: 1 },
      ]);
      if (url.endsWith("/api/v1/watchlist/platform/w2/import")) return json({
        added: ["AAPL"], already: ["EXMP"], skipped: [], themesCreated: [], themesUpdated: [], themesUnchanged: [], themesSkipped: [],
        notes: ["1 entries that are not shares (people) were left out"] });
      if (url.endsWith("/api/v1/watchlist")) return json(items);
      if (url.endsWith("/api/v1/themes")) return json(themes);
      if (url.endsWith("/api/v1/watchlist/overview")) return json({ canEdit, holdings: {
        known: true, stale: false, currency: "USD", asOf: "2026-11-23T09:00:00Z", watchlistValueCents: 40000, watchlistPnlCents: 1000, watchlistPositions: 1,
        notOnWatchlist: [{ instrumentId: 7, symbol: "MSFT", name: "Microsoft", market: "NYSE", positions: 1, units: "1.5", valueCents: 60000, costCents: 60000, pnlCents: 0, ours: 0, shorts: 0 }],
      } });
      if (url.endsWith("/api/v1/watchlist/quotes")) return json([]);
      if (url.endsWith("/api/v1/watchlist/theme-proposals")) return json({
        proposals: [{ instrumentId: 1002, symbol: "BARC.L", themeId: "t2", themeName: "Banks", score: 3, reasons: ["name contains “bank”"] }], unmatched: [], notes: [] });
      if (url.endsWith("/api/v1/watchlist/theme-assignments")) return json({ changedThemes: 1 });
      if (url.endsWith("/api/v1/watchlist/import")) return json({
        added: ["AAPL"], already: ["EXMP"], skipped: [{ instrumentId: 3, name: "SAP.DE", reason: "only shares listed in London (LSE), New York (NYSE/NASDAQ) or Sydney (ASX) can be traded" }],
        themesCreated: ["Space"], themesUpdated: [], themesUnchanged: ["Tech"], themesSkipped: [], notes: [] });
      return new Response("{}", { status: 404 });
    });
    render(<SessionProvider><ModeProvider><Watchlist /></ModeProvider></SessionProvider>);
    return posts;
  }
  afterEach(() => vi.restoreAllMocks());

  it("puts the theme dropdown right after Eligibility and assigns through the server", async () => {
    const posts = serve(true);
    const select = await screen.findByLabelText("Theme for BARC.L");
    const heads = screen.getAllByRole("columnheader").map((h) => h.textContent);
    expect(heads.indexOf("Theme")).toBe(heads.indexOf("Eligibility") + 1);
    expect(heads).toContain("Exchange");
    expect(heads).toContain("Holding");
    expect((screen.getByLabelText("Theme for EXMP") as HTMLSelectElement).value).toBe("t1");
    expect(Array.from((select as HTMLSelectElement).options).map((o) => o.text)).toEqual(["— none —", "Tech", "Banks"]);
    fireEvent.change(select, { target: { value: "t2" } });
    await waitFor(() => expect(posts).toContainEqual({ url: "/api/v1/watchlist/theme-assignments", body: { assignments: [{ instrumentId: 1002, themeId: "t2" }] } }));
  });

  it("filters by theme with chips", async () => {
    serve(true);
    const group = await screen.findByRole("group", { name: "Filter by theme" });
    await screen.findByText("BARC.L");
    expect(Array.from(group.querySelectorAll("button")).map((b) => b.textContent)).toEqual(["All", "Tech", "Banks", "No theme"]);
    fireEvent.click(screen.getByRole("button", { name: "Tech" }));
    expect(screen.getByRole("button", { name: "Tech" })).toHaveAttribute("aria-pressed", "true");
    expect(screen.queryByText("BARC.L")).not.toBeInTheDocument();
    expect(screen.getAllByText("EXMP").length).toBeGreaterThan(0);
    fireEvent.click(screen.getByRole("button", { name: "No theme" }));
    expect(screen.getByText("BARC.L")).toBeInTheDocument();
    expect(screen.queryByText("Example Corp")).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Banks" }));
    expect(screen.getByText("No share matches this filter.")).toBeInTheDocument();
  });

  it("links the export and shows the import result", async () => {
    const posts = serve(true);
    const link = await screen.findByRole("link", { name: "Export the watchlist and themes as JSON" });
    expect(link).toHaveAttribute("href", "/api/v1/watchlist/export");
    expect(link).toHaveAttribute("download");
    const file = new File([JSON.stringify({ version: 1, watchlist: [], themes: [] })], "w.json", { type: "application/json" });
    fireEvent.change(screen.getByLabelText("Watchlist file"), { target: { files: [file] } });
    const res = await screen.findByRole("status", { name: "Import result" });
    expect(res).toHaveTextContent("1 share added, 1 already there, 1 refused; themes 1 created, 0 updated, 1 unchanged.");
    expect(res).toHaveTextContent("Not added: SAP.DE — only shares listed in London");
    expect(res.className).toContain("alert-warning");
    expect(posts).toContainEqual({ url: "/api/v1/watchlist/import", body: { version: 1, watchlist: [], themes: [] } });
  });

  it("imports one of the account's eToro watchlists (R46)", async () => {
    const posts = serve(true);
    fireEvent.click(await screen.findByRole("button", { name: "Import a watchlist from eToro" }));
    const pick = await screen.findByLabelText("eToro watchlist");
    expect((pick as HTMLSelectElement).value).toBe("w2"); // the default list first
    expect(Array.from((pick as HTMLSelectElement).options).map((o) => o.text)).toEqual(["Tech (3 shares)", "My Watchlist (2 shares)"]);
    expect(screen.getByText(/1 people on this list are left out/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Import" }));
    const res = await screen.findByRole("status", { name: "Import result" });
    expect(res).toHaveTextContent("1 share added, 1 already there");
    expect(posts).toContainEqual({ url: "/api/v1/watchlist/platform/w2/import", body: null });
    expect(screen.queryByLabelText("eToro watchlist")).not.toBeInTheDocument();
  });

  it("refuses a file that is not JSON before sending it", async () => {
    const posts = serve(true);
    await screen.findByRole("link", { name: "Export the watchlist and themes as JSON" });
    fireEvent.change(screen.getByLabelText("Watchlist file"), { target: { files: [new File(["not json"], "w.json")] } });
    expect(await screen.findByText("The file is not JSON.")).toBeInTheDocument();
    expect(posts.filter((p) => p.url.endsWith("/import"))).toEqual([]);
  });

  it("proposes themes and changes nothing until accepted", async () => {
    const posts = serve(true);
    fireEvent.click(await screen.findByRole("button", { name: "Auto-assign themes" }));
    const card = await screen.findByLabelText("Proposed themes");
    expect(card).toHaveTextContent("BARC.L");
    expect(card).toHaveTextContent("score 3: name contains “bank”");
    expect(posts.filter((p) => p.url.endsWith("/theme-assignments"))).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Accept every proposal" }));
    await waitFor(() => expect(posts).toContainEqual({ url: "/api/v1/watchlist/theme-assignments", body: { assignments: [{ instrumentId: 1002, themeId: "t2" }] } }));
  });

  it("shows holdings, the exchange and who opened them", async () => {
    serve(true);
    const cell = await screen.findByLabelText("Holding of EXMP");
    expect(cell).toHaveTextContent("2 sh · $400.00");
    expect(cell).toHaveTextContent("P&L $10.00");
    expect(cell).toHaveTextContent("cactusTrade");
    expect(screen.getByText("NYSE / NASDAQ")).toBeInTheDocument();
    expect(screen.getByText("LSE")).toBeInTheDocument();
    const sum = screen.getByLabelText("Holdings");
    expect(sum).toHaveTextContent("$400.00 on watchlist shares (1 position, P&L $10.00)");
    expect(screen.getByLabelText("Held but not on the watchlist")).toHaveTextContent("MSFT 1.5 sh · $600.00");
  });

  it("shows the theme as text and no tools when the server says the caller cannot edit", async () => {
    serve(false);
    expect(await screen.findByText("Tech", { selector: "td span" })).toBeInTheDocument();
    expect(screen.queryByLabelText("Theme for EXMP")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Auto-assign themes" })).not.toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Export the watchlist and themes as JSON" })).not.toBeInTheDocument();
  });
});

describe("Watchlist on a Trading 212 practice account (R45.11)", () => {
  it("shows each share's match and lets the owner confirm it after seeing both names and the ISIN", async () => {
    let confirmed = false;
    const posts: string[] = [];
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
      const url = String(input);
      const json = (v: unknown) => new Response(JSON.stringify(v), { status: 200 });
      if (url.endsWith("/api/v1/auth/me")) return json(me);
      if (url.endsWith("/api/v1/system/status")) return json({ version: "0.1.0", build: "1", mode: "demo", agentState: "stopped", database: "sqlite", startedAt: "", now: "",
        account: { id: "t1", name: "Practice", ownerId: "1", ownerName: "admin", mode: "demo", platform: "trading212", envKeys: false, access: "owner", createdAt: "", currency: "GBP" } });
      if (url.endsWith("/match/confirm") && init?.method === "POST") {
        posts.push(url);
        confirmed = true;
        return json({ instrumentId: 1, ticker: "BARCl_EQ", isin: "GB0031348658", status: "confirmed" });
      }
      if (url.endsWith("/api/v1/watchlist")) return json([{
        instrument: { instrumentId: 1, symbol: "BARC.L", name: "Barclays", type: "Stocks", market: "LSE" },
        eligibility: { realLong: false, reason: "(unchecked)" }, addedBy: "admin", addedAt: "2026-11-23T09:00:00Z",
        brokerMatch: { instrumentId: 1, ticker: "BARCl_EQ", isin: "GB0031348658", name: "Barclays PLC", currency: "GBX", status: confirmed ? "confirmed" : "unconfirmed" },
      }]);
      return new Response("{}", { status: 404 });
    });
    render(<SessionProvider><ModeProvider><Watchlist /></ModeProvider></SessionProvider>);
    expect(await screen.findByText("BARCl_EQ")).toBeInTheDocument();
    expect(screen.getByText("not confirmed")).toBeInTheDocument();
    fireEvent.click(await screen.findByRole("button", { name: "Confirm the Trading 212 match of BARC.L" }));
    const dialog = await screen.findByRole("dialog");
    expect(dialog).toHaveTextContent("Barclays PLC");
    expect(dialog).toHaveTextContent("GB0031348658");
    fireEvent.click(screen.getByRole("button", { name: /Confirm the match/ }));
    await waitFor(() => expect(posts).toEqual(["/api/v1/watchlist/1/match/confirm"]));
    expect(await screen.findByText("confirmed")).toBeInTheDocument();
  });
});
