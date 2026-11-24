import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { MemoryRouter } from "react-router-dom";
import { SessionProvider } from "../lib/session";
import { TradingStatements } from "./TradingStatements";
import { System } from "../pages/System";
import { ModeProvider } from "../lib/mode";

const me = (role: string, permissions: string[]) => ({
  user: { id: "u1", username: "albie", role, disabled: false, mustChangePassword: false, createdAt: "", updatedAt: "" },
  permissions, via: "session", csrfToken: "c",
});
const admin = me("admin", ["view", "agent.control", "settings.write"]);
const operator = me("operator", ["view", "agent.control"]);

const accounts = [
  { id: "a1", name: "Main demo", platform: "etoro", mode: "demo" },
  { id: "t1", name: "ISA", platform: "trading212", mode: "real" },
];
const weekly = {
  id: "s1", ownerId: "u1", ownerName: "albie", period: "weekly", accountIds: [], accounts, recipients: ["me@example.com", "acct@example.com"],
  includeCombined: true, includeAccounts: true, includeExchanges: false, enabled: true, lastPeriod: "2026-W47",
  lastSentAt: "2026-11-21T06:00:00Z", lastError: "", nextSendAt: "2026-11-28T06:00:00Z", createdAt: "", updatedAt: "",
};
const others = {
  ...weekly, id: "s2", ownerId: "u9", ownerName: "olivia", period: "monthly", accountIds: ["o1"], accounts: [{ id: "o1", name: "Olivia demo", platform: "etoro", mode: "demo" }],
  recipients: ["boss@example.com"], enabled: false, lastSentAt: undefined, nextSendAt: undefined, lastError: "the statements are too large",
};

interface Call { method: string; url: string; body?: unknown }

function mock(page: unknown, who = admin) {
  const calls: Call[] = [];
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
    const url = String(input);
    const method = init?.method ?? "GET";
    calls.push({ method, url, body: init?.body ? JSON.parse(String(init.body)) : undefined });
    const json = (v: unknown, status = 200) => new Response(JSON.stringify(v), { status });
    if (url.endsWith("/api/v1/auth/me")) return json(who);
    if (url.endsWith("/api/v1/system/status")) return json({ version: "1", build: "2", mode: "demo", agentState: "stopped", database: "sqlite", startedAt: "2026-11-23T09:00:00Z", now: "2026-11-23T09:00:00Z" });
    if (url.endsWith("/api/v1/statements/schedules") && method === "GET") return json(page);
    if (url.endsWith("/api/v1/statements/schedules") && method === "POST") return json({ ...weekly, id: "s3" }, 201);
    if (url.includes("/send-now")) {
      return json({ period: "2026-W47", from: "", to: "", title: "16–20 November 2026", recipients: 2, files: ["a.pdf", "b.pdf", "c.pdf"] });
    }
    if (method === "DELETE") return new Response(null, { status: 204 });
    if (method === "PUT") return json(weekly);
    return json({ title: "Not found", status: 404 }, 404);
  });
  return calls;
}

function show(page: unknown, who = admin) {
  const calls = mock(page, who);
  render(<MemoryRouter><SessionProvider><TradingStatements /></SessionProvider></MemoryRouter>);
  return calls;
}

afterEach(() => vi.restoreAllMocks());

describe("TradingStatements", () => {
  it("shows schedules as cards with compact icon buttons first", async () => {
    show({ schedules: [weekly, others], accounts, mailServer: true, canFixMail: true });
    const group = await screen.findByRole("group", { name: "Actions for weekly statements to me@example.com, acct@example.com" });
    const buttons = within(group).getAllByRole("button").concat(within(group).getAllByRole("link"));
    expect(buttons.map((b) => b.getAttribute("title"))).toEqual(expect.arrayContaining(["Send now (the last complete period)", "Edit", "Delete"]));
    expect(group.firstElementChild).toHaveAttribute("aria-label", "Send now: weekly statements to me@example.com, acct@example.com");
    const preview = within(group).getByRole("link", { name: /Preview/ });
    expect(preview).toHaveAttribute("href", "/api/v1/statements/preview.pdf?period=weekly&scheduleId=s1");
    expect(screen.getByText("Weekly", { selector: "strong" })).toBeInTheDocument();
    expect(screen.getByText("me@example.com")).toBeInTheDocument();
    expect(screen.getAllByText("Per account")).toHaveLength(2);
    expect(screen.getByText(/all \(2\):/)).toBeInTheDocument();
    expect(screen.getByText(/\(2026-W47\)/)).toBeInTheDocument();
    // Someone else's schedule (an admin sees it): owner, off, never sent, its last error.
    expect(screen.getByText("olivia")).toBeInTheDocument();
    expect(screen.getByText("off")).toBeInTheDocument();
    expect(screen.getByText("never")).toBeInTheDocument();
    expect(screen.getByText("switched off")).toBeInTheDocument();
    expect(screen.getByText(/Last send failed: the statements are too large/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /What is sent and when/ })).toBeInTheDocument();
    expect(screen.queryByText(/No mail server/)).not.toBeInTheDocument();
  });

  it("offers the e-mail fix when no mail server is set up, only to who can fix it", async () => {
    show({ schedules: [], accounts, mailServer: false, canFixMail: true });
    expect(await screen.findByText(/No mail server is set up/)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /Set up e-mail/ })).toHaveAttribute("href", "/email");
    expect(screen.getByText("No statement schedules yet.")).toBeInTheDocument();
  });

  it("tells a user who cannot fix the mail server to ask an administrator", async () => {
    show({ schedules: [], accounts: [], mailServer: false, canFixMail: false }, operator);
    expect(await screen.findByText(/ask an administrator/)).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: /Set up e-mail/ })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Add schedule/ })).toBeDisabled(); // owns no account
  });

  it("adds a schedule with chosen accounts and recipient chips", async () => {
    const calls = show({ schedules: [], accounts, mailServer: true, canFixMail: true });
    fireEvent.click(await screen.findByRole("button", { name: /Add schedule/ }));
    const form = screen.getByRole("form", { name: "New statement schedule" });
    fireEvent.change(within(form).getByLabelText("Period"), { target: { value: "monthly" } });
    expect(within(form).getByText(/on the 1st at 06:00 UTC/)).toBeInTheDocument();
    fireEvent.click(within(form).getByLabelText(/ISA/));
    const input = within(form).getByLabelText(/^Recipients \(/);
    fireEvent.change(input, { target: { value: " Me@Example.com " } });
    fireEvent.keyDown(input, { key: "Enter" });
    fireEvent.change(input, { target: { value: "acct@example.com" } });
    fireEvent.click(within(form).getByRole("button", { name: "Add recipient" }));
    fireEvent.change(input, { target: { value: "gone@example.com" } });
    fireEvent.keyDown(input, { key: "Enter" });
    fireEvent.click(within(form).getByRole("button", { name: "Remove gone@example.com" }));
    expect(within(form).getByText("Recipients (2/10)")).toBeInTheDocument();
    fireEvent.click(within(form).getByLabelText("One per exchange (with trades or positions)"));
    fireEvent.click(within(form).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(calls.some((c) => c.method === "POST")).toBe(true));
    expect(calls.find((c) => c.method === "POST")?.body).toEqual({
      period: "monthly", accountIds: ["t1"], recipients: ["me@example.com", "acct@example.com"], includeCombined: true,
      includeAccounts: false, includeExchanges: true, enabled: true,
    });
    expect(await screen.findByText(/Schedule added/)).toBeInTheDocument();
  });

  it("stops adding recipients at ten", async () => {
    show({ schedules: [], accounts, mailServer: true, canFixMail: true });
    fireEvent.click(await screen.findByRole("button", { name: /Add schedule/ }));
    const input = screen.getByLabelText(/^Recipients \(/);
    for (let i = 0; i < 10; i++) {
      fireEvent.change(input, { target: { value: `r${i}@example.com` } });
      fireEvent.keyDown(input, { key: "Enter" });
    }
    expect(screen.getByText("Recipients (10/10)")).toBeInTheDocument();
    expect(input).toBeDisabled();
  });

  it("sends now, edits and deletes after confirming", async () => {
    const calls = show({ schedules: [weekly], accounts, mailServer: true, canFixMail: true });
    fireEvent.click(await screen.findByRole("button", { name: /^Send now:/ }));
    expect(await screen.findByText(/Weekly statement for 16–20 November 2026 queued for 2 recipients \(3 PDFs\)/)).toBeInTheDocument();
    expect(calls.some((c) => c.method === "POST" && c.url.endsWith("/api/v1/statements/schedules/s1/send-now"))).toBe(true);

    fireEvent.click(screen.getByRole("button", { name: /^Edit:/ }));
    const form = screen.getByRole("form", { name: "Edit statement schedule" });
    expect(within(form).getByText("me@example.com")).toBeInTheDocument();
    fireEvent.click(within(form).getByLabelText("On"));
    fireEvent.click(within(form).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(calls.some((c) => c.method === "PUT")).toBe(true));
    expect(calls.find((c) => c.method === "PUT")?.body).toMatchObject({ period: "weekly", enabled: false, recipients: ["me@example.com", "acct@example.com"] });

    fireEvent.click(await screen.findByRole("button", { name: /^Delete:/ }));
    expect(screen.getByRole("dialog")).toHaveTextContent("E-mails already in the outbox are still delivered.");
    expect(calls.some((c) => c.method === "DELETE")).toBe(false);
    fireEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: /Delete/ }));
    await waitFor(() => expect(calls.some((c) => c.method === "DELETE" && c.url.endsWith("/schedules/s1"))).toBe(true));
    expect(await screen.findByText("Schedule deleted.")).toBeInTheDocument();
  });

  it("shows a refused send with the server's fix", async () => {
    mock({ schedules: [weekly], accounts, mailServer: true, canFixMail: true });
    const fetchMock = vi.mocked(globalThis.fetch);
    const base = fetchMock.getMockImplementation()!;
    fetchMock.mockImplementation(async (input, init) => {
      if (String(input).includes("/send-now")) {
        return new Response(JSON.stringify({ title: "Unavailable", status: 503, detail: "No mail server", missing: "email_server", canFix: true }), { status: 503 });
      }
      return base(input, init);
    });
    render(<MemoryRouter><SessionProvider><TradingStatements /></SessionProvider></MemoryRouter>);
    fireEvent.click(await screen.findByRole("button", { name: /^Send now:/ }));
    expect(await screen.findByText("No mail server")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /Set up e-mail/ })).toBeInTheDocument();
  });

  it("builds the preview link from the chosen period, day and statement", async () => {
    show({ schedules: [], accounts, mailServer: true, canFixMail: true });
    const group = await screen.findByRole("group", { name: "Preview a statement" });
    const link = within(group).getByRole("link", { name: /Preview PDF/ });
    expect(link).toHaveAttribute("href", "/api/v1/statements/preview.pdf?period=weekly&scope=combined");
    fireEvent.change(within(group).getByLabelText("Period"), { target: { value: "daily" } });
    fireEvent.change(within(group).getByLabelText("Containing the day"), { target: { value: "2026-11-24" } });
    fireEvent.change(within(group).getByLabelText("Statement"), { target: { value: "account" } });
    expect(link).toHaveAttribute("href", "/api/v1/statements/preview.pdf?period=daily&from=2026-11-24&scope=account&accountId=a1");
    fireEvent.change(within(group).getByLabelText("Statement"), { target: { value: "exchange" } });
    fireEvent.change(within(group).getByLabelText("Exchange"), { target: { value: "ASX" } });
    expect(link).toHaveAttribute("href", "/api/v1/statements/preview.pdf?period=daily&from=2026-11-24&scope=exchange&exchange=ASX");
  });
});

describe("System page", () => {
  it("shows the statements to everyone and the restart only to admins", async () => {
    mock({ schedules: [], accounts, mailServer: true, canFixMail: false }, operator);
    render(<MemoryRouter><SessionProvider><ModeProvider><System /></ModeProvider></SessionProvider></MemoryRouter>);
    expect(await screen.findByRole("heading", { name: "Trading statements" })).toBeInTheDocument();
    await screen.findByText("No statement schedules yet.");
    expect(screen.queryByRole("button", { name: /Restart/ })).not.toBeInTheDocument();
  });

  it("keeps the admin actions for admins", async () => {
    mock({ schedules: [], accounts, mailServer: true, canFixMail: true }, admin);
    render(<MemoryRouter><SessionProvider><ModeProvider><System /></ModeProvider></SessionProvider></MemoryRouter>);
    expect(await screen.findByRole("button", { name: /Restart/ })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Trading statements" })).toBeInTheDocument();
  });
});
