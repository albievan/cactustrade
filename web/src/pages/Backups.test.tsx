import { webcrypto } from "node:crypto";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { MemoryRouter } from "react-router-dom";
import { SessionProvider } from "../lib/session";
import { Backups } from "./Backups";

const me = (role: string, permissions: string[]) => ({
  user: { id: "u1", username: "albie", role, disabled: false, mustChangePassword: false, createdAt: "", updatedAt: "" },
  permissions, via: "session", csrfToken: "c",
});
const admin = me("admin", ["view", "agent.control", "settings.write"]);
const operator = me("operator", ["view", "agent.control"]);

const RCPT = "age1q73he0q5yzfu3d64msd3p6rvksnrwjk3d2598mgtmlqt9wrdr37q2vrn72";
const file = { name: "cactustrade-20261128T013000Z.db.age", size: 2048, at: "2026-11-28T01:30:00Z" };
const status = {
  available: true, folder: "/srv/cactustrade/data/backups", driver: "sqlite",
  schedule: { kind: "daily", at: "02:30", zone: "Europe/London", keep: 7 },
  nextRun: "2026-11-29T02:30:00Z", nextRunLocal: "Sun 29 Nov 2026 02:30 GMT",
  recipients: [{ key: RCPT, fingerprint: "sha256:0011223344556677", source: "ui" }], recipientSource: "ui",
  last: { at: "2026-11-28T01:30:00Z", trigger: "schedule", ok: true, file: file.name, bytes: 2048, ms: 120 },
  files: [file], running: false,
};

interface Call { method: string; url: string; body?: string }

function mock(opts: { who?: unknown; page?: unknown; refuse?: { title: string; detail: string; field?: string } } = {}) {
  const calls: Call[] = [];
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
    const url = String(input);
    const method = init?.method ?? "GET";
    calls.push({ method, url, body: init?.body ? String(init.body) : undefined });
    const json = (v: unknown, code = 200) => new Response(JSON.stringify(v), { status: code, headers: { "Content-Type": "application/json" } });
    if (url.endsWith("/api/v1/auth/me")) return json(opts.who ?? admin);
    if (url.endsWith("/api/v1/backups") && method === "GET") return json(opts.page ?? status);
    if (opts.refuse && method !== "GET") {
      return new Response(JSON.stringify({ status: 400, ...opts.refuse }), { status: 400, headers: { "Content-Type": "application/problem+json" } });
    }
    if (url.endsWith("/api/v1/backups/schedule")) return json({ ...status, schedule: JSON.parse(String(init?.body)) });
    if (url.endsWith("/api/v1/backups/recipients")) {
      const rs: string[] = JSON.parse(String(init?.body)).recipients;
      return json({ ...status, recipients: rs.map((k) => ({ key: k, fingerprint: "sha256:x", source: "ui" })) });
    }
    if (url.endsWith("/api/v1/backups/run")) return json({ name: "cactustrade-20261128T090000Z.db.age", size: 4096, at: "2026-11-28T09:00:00Z" });
    return json({ title: "Not found", status: 404 }, 404);
  });
  return calls;
}

function show(opts: Parameters<typeof mock>[0] = {}) {
  const calls = mock(opts);
  render(<MemoryRouter><SessionProvider><Backups /></SessionProvider></MemoryRouter>);
  return calls;
}

beforeEach(() => vi.stubGlobal("crypto", webcrypto));
afterEach(() => { vi.restoreAllMocks(); vi.unstubAllGlobals(); });

describe("Backups", () => {
  it("is hidden without settings.write and asks the server nothing", async () => {
    const calls = show({ who: operator });
    expect(await screen.findByText("Only administrators manage backups.")).toBeInTheDocument();
    expect(calls.some((c) => c.url.includes("/api/v1/backups"))).toBe(false);
  });

  it("shows the folder, the next run in local time and UTC, the last run and the files with download links", async () => {
    show();
    expect(await screen.findByTestId("backup-folder")).toHaveTextContent("/srv/cactustrade/data/backups");
    expect(screen.getByText("Folder (set at deploy time)")).toBeInTheDocument();
    expect(screen.getByTestId("backup-next")).toHaveTextContent("Sun 29 Nov 2026 02:30 GMT");
    expect(screen.getByTestId("backup-next")).toHaveTextContent("2026-11-29 02:30 UTC");
    expect(screen.getByTestId("backup-last")).toHaveTextContent(`OK, ${file.name}`);
    const link = screen.getByRole("link", { name: /Download/ });
    expect(link).toHaveAttribute("href", `/api/v1/backups/files/${file.name}`);
    expect(screen.getByText("2.0 KiB")).toBeInTheDocument();
  });

  it("posts the schedule editor's values", async () => {
    const calls = show();
    fireEvent.change(await screen.findByLabelText("Schedule"), { target: { value: "every" } });
    fireEvent.change(screen.getByLabelText("Hours"), { target: { value: "5" } });
    fireEvent.change(screen.getByLabelText("Keep newest"), { target: { value: "3" } });
    fireEvent.click(screen.getByRole("button", { name: /Save schedule/ }));
    await screen.findByText("Schedule saved and audited.");
    const put = calls.find((c) => c.method === "PUT" && c.url.endsWith("/api/v1/backups/schedule"));
    expect(JSON.parse(put?.body ?? "{}")).toMatchObject({ kind: "every", everyHours: 5, keep: 3, zone: "Europe/London" });
  });

  it("shows the server's refusal", async () => {
    show({ refuse: { title: "Invalid input", detail: "recipients: add an age public key first: backups are always encrypted", field: "recipients" } });
    fireEvent.click(await screen.findByRole("button", { name: /Save schedule/ }));
    expect(await screen.findByRole("alert")).toHaveTextContent("add an age public key first");
  });

  it("backs up now and reloads the list", async () => {
    const calls = show();
    fireEvent.click(await screen.findByRole("button", { name: /Back up now/ }));
    expect(await screen.findByText(/Backup cactustrade-20261128T090000Z\.db\.age written \(4\.0 KiB\)/)).toBeInTheDocument();
    expect(calls.filter((c) => c.method === "POST" && c.url.endsWith("/api/v1/backups/run"))).toHaveLength(1);
    await waitFor(() => expect(calls.filter((c) => c.method === "GET" && c.url.endsWith("/api/v1/backups")).length).toBe(2));
  });

  it("says when the deployment has no backup folder", async () => {
    show({ page: { ...status, available: false, files: [], recipients: [], recipientSource: "" } });
    expect(await screen.findByText(/backups from the app are unavailable/)).toBeInTheDocument();
  });

  it("makes a key pair in the browser and sends only the public key", async () => {
    const calls = show();
    fireEvent.click(await screen.findByRole("button", { name: /Generate a key pair/ }));
    const identity = (await screen.findByTestId("identity")).textContent ?? "";
    expect(identity).toMatch(/^AGE-SECRET-KEY-1[0-9A-Z]{58}$/);
    const add = screen.getByRole("button", { name: "Add the public key" });
    expect(add).toBeDisabled(); // until the admin says the private key is saved
    fireEvent.click(screen.getByLabelText(/I have saved the private key/));
    fireEvent.click(add);
    await screen.findByText(/Public key added/);
    expect(screen.queryByTestId("identity")).not.toBeInTheDocument();
    const put = calls.find((c) => c.method === "PUT" && c.url.endsWith("/api/v1/backups/recipients"));
    const sent: string[] = JSON.parse(put?.body ?? "{}").recipients;
    expect(sent).toHaveLength(2);
    expect(sent[0]).toBe(RCPT);
    expect(sent[1]).toMatch(/^age1[0-9a-z]{58}$/);
    for (const c of calls) {
      expect(c.body ?? "").not.toContain(identity);
      expect(c.body ?? "").not.toContain("AGE-SECRET-KEY");
      expect(c.url).not.toContain("AGE-SECRET-KEY");
    }
  });

  it("shows the age-keygen command without X25519", async () => {
    vi.stubGlobal("crypto", { subtle: { generateKey: () => Promise.reject(new Error("NotSupportedError")) } });
    show();
    fireEvent.click(await screen.findByRole("button", { name: /Generate a key pair/ }));
    expect(await screen.findByText("age-keygen -o cactustrade-backup.agekey")).toBeInTheDocument();
  });
});
