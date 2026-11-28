import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { T212Setup } from "./T212Setup";
import type { Account } from "../lib/api";

const acct = (over: Partial<Account>): Account => ({
  id: "x", name: "x", ownerId: "1", ownerName: "admin", mode: "demo", platform: "etoro", envKeys: false, access: "owner", createdAt: "", ...over,
});
const practice = acct({ id: "t1", name: "Practice", platform: "trading212", agentTradable: true });
const accounts = [
  practice,
  acct({ id: "e1", name: "Main demo" }),
  acct({ id: "e2", name: "Real", mode: "real" }),
  acct({ id: "e3", name: "Someone else's demo", ownerId: "2" }),
];

afterEach(() => vi.restoreAllMocks());

describe("T212Setup (R45.11)", () => {
  it("offers the owner's eToro demo accounts as the price source and refreshes the instrument list", async () => {
    const calls: { method: string; url: string; body?: unknown }[] = [];
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
      const url = String(input);
      const method = init?.method ?? "GET";
      calls.push({ method, url, body: init?.body ? JSON.parse(String(init.body)) : undefined });
      const json = (v: unknown) => new Response(JSON.stringify(v), { status: 200 });
      if (url.endsWith("/price-source")) return new Response(null, { status: 204 });
      if (url.endsWith("/t212/instruments/refresh")) return json({ count: 15000, stale: false, fetchedAt: "2026-11-28T09:00:00Z" });
      if (url.endsWith("/t212/instruments")) return json({ count: 0, stale: true });
      return new Response("{}", { status: 404 });
    });
    const changed = vi.fn();
    render(<T212Setup account={practice} accounts={accounts} owner onChanged={changed} />);
    expect(await screen.findByText("never fetched")).toBeInTheDocument();
    const select = screen.getByLabelText("Prices and signals from");
    const options = Array.from((select as HTMLSelectElement).options).map((o) => o.textContent);
    expect(options).toEqual(["— none: nothing is bought —", "Main demo (eToro demo)"]); // never a real or another owner's account
    fireEvent.change(select, { target: { value: "e1" } });
    await waitFor(() => expect(changed).toHaveBeenCalled());
    expect(calls.find((c) => c.method === "PUT")).toMatchObject({ url: "/api/v1/accounts/t1/price-source", body: { accountId: "e1" } });
    fireEvent.click(screen.getByRole("button", { name: "Refresh" }));
    expect(await screen.findByText("15000 instruments stored.")).toBeInTheDocument();
    expect(screen.getByText(/fresh · fetched/)).toBeInTheDocument();
  });

  it("shows a viewer the price source without changing it", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async () => new Response(JSON.stringify({ count: 0, stale: false, fetchedAt: "2026-11-28T09:00:00Z" }), { status: 200 }));
    render(<T212Setup account={{ ...practice, access: "view", priceSourceAccountId: "e1" }} accounts={accounts} owner={false} onChanged={() => undefined} />);
    expect(await screen.findByText("Main demo")).toBeInTheDocument();
    expect(screen.queryByRole("combobox")).toBeNull();
    expect(screen.queryByRole("button", { name: "Refresh" })).toBeNull();
  });
});
