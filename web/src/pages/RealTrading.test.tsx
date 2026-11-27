import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ModeProvider } from "../lib/mode";
import { RealTrading } from "./RealTrading";
import source from "./RealTrading.tsx?raw";

const real = { id: "r1", name: "Live", ownerId: "1", ownerName: "admin", mode: "real", platform: "etoro", envKeys: false, access: "owner", createdAt: "" };
const demo = { ...real, id: "d1", name: "Practice", mode: "demo" };
const t212 = { ...real, id: "t1", name: "ISA", platform: "trading212" };
const status = { version: "0.1.0", build: "1", mode: "demo", agentState: "stopped", database: "sqlite", startedAt: "", now: "", account: demo, realArmed: [] };

function gate(over: Record<string, unknown> = {}) {
  return {
    account: real, gate: { accountId: "r1", state: "locked", evidenceDays: 0, version: 0 }, armed: false, enabled: false,
    demoAccountId: "d1", demoCandidates: [demo],
    checks: [
      { name: "real_account", pass: true, detail: "an eToro real-money account" },
      { name: "demo_evidence", pass: false, detail: "19 qualifying demo trading days in the last 60 risk days; 20 needed" },
    ],
    evidence: { days: ["2026-11-20", "2026-11-21"], count: 19, first: "2026-10-20", last: "2026-11-21" },
    rule: { minDays: 20, windowDays: 60, maxAgeDays: 7 }, phrase: "ENABLE REAL TRADING Live", canArm: true, canDisarm: true,
    canSetDays: true, minDaysFloor: 1, defaultMinDays: 5,
    ...over,
  };
}

interface Call { method: string; url: string; body?: unknown }

function serve(g: ReturnType<typeof gate>, armAnswer?: { status: number; body: unknown }) {
  const calls: Call[] = [];
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
    const url = String(input);
    const method = init?.method ?? "GET";
    calls.push({ method, url, body: init?.body ? JSON.parse(String(init.body)) : undefined });
    const json = (v: unknown, s = 200) => new Response(JSON.stringify(v), { status: s });
    if (url.endsWith("/api/v1/system/status")) return json(status);
    if (url.endsWith("/api/v1/accounts")) return json([real, demo, t212]);
    if (url.includes("/real-gate/arm")) return json(armAnswer?.body ?? {}, armAnswer?.status ?? 200);
    if (url.includes("/real-gate/demo-days")) return json({ ...g, rule: { ...g.rule, minDays: JSON.parse(String(init?.body)).minDays } });
    if (url.includes("/real-gate/disarm")) return json({ accountId: "r1", state: "locked", evidenceDays: 0, version: 0 });
    if (url.includes("/real-gate")) return json(g);
    return new Response("{}", { status: 404 });
  });
  render(<ModeProvider><RealTrading /></ModeProvider>);
  return calls;
}

afterEach(() => vi.restoreAllMocks());

describe("RealTrading", () => {
  it("shows one card per real eToro account with the server's checklist and evidence", async () => {
    serve(gate());
    const list = await screen.findByRole("list", { name: "Checklist for Live" });
    expect(within(list).getByText("an eToro real-money account")).toBeInTheDocument();
    expect(within(list).getByText(/19 qualifying demo trading days/)).toBeInTheDocument();
    expect(within(list).getByLabelText("fail")).toBeInTheDocument();
    expect(screen.getByText(/not enabled in this build/)).toBeInTheDocument();
    expect(screen.getByLabelText("Evidence")).toHaveTextContent("19 of 20 needed");
    expect(screen.getByLabelText("Demo account whose history is the evidence")).toHaveValue("d1");
    expect(screen.queryByText("ISA")).not.toBeInTheDocument(); // view-only platforms have no gate
    expect(screen.getByText("locked")).toBeInTheDocument();
  });

  it("asks the server again for another demo account", async () => {
    const calls = serve(gate({ demoCandidates: [demo, { ...demo, id: "d2", name: "Other" }] }));
    fireEvent.change(await screen.findByLabelText("Demo account whose history is the evidence"), { target: { value: "d2" } });
    await waitFor(() => expect(calls.some((c) => c.url.endsWith("/real-gate?demoAccountId=d2"))).toBe(true));
  });

  it("needs the phrase and the password, sends them, and shows the server's refusal", async () => {
    const calls = serve(gate(), { status: 403, body: { status: 403, title: "Real trading is not enabled in this build", detail: "real trading needs the owner's written consent (PLAN 7.18): this build refuses to arm it" } });
    fireEvent.click(await screen.findByText("Arm real trading…"));
    const go = screen.getByRole("button", { name: "Arm real trading" });
    expect(go).toBeDisabled();
    fireEvent.change(screen.getByLabelText(/Type/), { target: { value: "ENABLE REAL TRADING Live" } });
    expect(go).toBeDisabled(); // the password too
    fireEvent.change(screen.getByLabelText("Your password"), { target: { value: "secret password" } });
    expect(go).toBeEnabled();
    fireEvent.click(go);
    expect(await screen.findByRole("alert")).toHaveTextContent("this build refuses to arm it");
    const arm = calls.find((c) => c.url.endsWith("/api/v1/accounts/r1/real-gate/arm"));
    expect(arm?.method).toBe("POST");
    expect(arm?.body).toEqual({ demoAccountId: "d1", confirm: "ENABLE REAL TRADING Live", password: "secret password" });
    expect(screen.getByLabelText("Your password")).toHaveValue(""); // cleared after a refusal
  });

  it("disarms after a confirmation", async () => {
    const calls = serve(gate({ armed: true, gate: { accountId: "r1", state: "armed", evidenceDays: 20, version: 1, expiresAt: "2026-12-24T00:00:00Z" } }));
    expect(await screen.findByText("ARMED")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Disarm" }));
    fireEvent.change(screen.getByLabelText("Reason (optional)"), { target: { value: "drill" } });
    expect(calls.some((c) => c.url.includes("/disarm"))).toBe(false);
    fireEvent.click(screen.getAllByRole("button", { name: "Disarm" }).at(-1)!);
    expect(await screen.findByText("Real trading is disarmed on Live.")).toBeInTheDocument();
    const d = calls.filter((c) => c.url.endsWith("/api/v1/accounts/r1/real-gate/disarm"));
    expect(d).toHaveLength(1);
    expect(d[0].body).toEqual({ reason: "drill" });
  });

  it("shows only the buttons the server allows", async () => {
    serve(gate({ canArm: false, canDisarm: false }));
    await screen.findByRole("list", { name: "Checklist for Live" });
    expect(screen.queryByText("Arm real trading…")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Disarm" })).not.toBeInTheDocument();
  });

  it("has no gate logic of its own", () => {
    for (const banned of [".role", ".access", ".via", "permissions", "minDays -", ">= st.rule", "count >=", "ENABLE REAL TRADING "]) {
      expect(source).not.toContain(banned);
    }
  });

  it("lets the account's admin set the demo days needed (D36), within the server's range", async () => {
    const calls = serve(gate());
    const input = await screen.findByLabelText("Qualifying demo days needed before arming");
    const save = screen.getByRole("button", { name: "Save" });
    expect(input).toHaveValue(20);
    expect(save).toBeDisabled(); // unchanged
    expect(screen.getByText(/1–60 days \(default 5\)/)).toBeInTheDocument();
    fireEvent.change(input, { target: { value: "0" } });
    expect(save).toBeDisabled(); // below the floor
    fireEvent.change(input, { target: { value: "10" } });
    fireEvent.click(save);
    expect(await screen.findByText("Arming now needs 10 qualifying demo days (audited).")).toBeInTheDocument();
    expect(calls).toContainEqual({ method: "PUT", url: "/api/v1/accounts/r1/real-gate/demo-days", body: { minDays: 10 } });
  });

  it("hides the demo days setting from anyone else", async () => {
    serve(gate({ canSetDays: false }));
    await screen.findByRole("list", { name: "Checklist for Live" });
    expect(screen.queryByLabelText("Qualifying demo days needed before arming")).not.toBeInTheDocument();
  });
});
