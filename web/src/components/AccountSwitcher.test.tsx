import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { AccountSwitcher } from "./AccountSwitcher";
import type { Account } from "../lib/api";

const acct = (over: Partial<Account>): Account => ({
  id: "x", name: "x", ownerId: "1", ownerName: "admin", mode: "demo", platform: "etoro", envKeys: false, access: "owner", createdAt: "", ...over,
});

afterEach(() => vi.restoreAllMocks());

describe("AccountSwitcher (R45.11)", () => {
  it("lists the accounts the agent trades: eToro and a Trading 212 practice account, never a real Trading 212 one", async () => {
    const accounts = [
      acct({ id: "e1", name: "Main demo", agentTradable: true }),
      acct({ id: "t1", name: "Practice", platform: "trading212", agentTradable: true }),
      acct({ id: "t2", name: "ISA", platform: "trading212", mode: "real", agentTradable: false }),
      acct({ id: "s1", name: "Saxo SIM", platform: "saxo", agentTradable: false }),
    ];
    vi.spyOn(globalThis, "fetch").mockImplementation(async () => new Response(JSON.stringify(accounts), { status: 200 }));
    render(<MemoryRouter><AccountSwitcher /></MemoryRouter>);
    expect(await screen.findByRole("option", { name: /Trading 212 · Practice/ })).toBeInTheDocument();
    expect(screen.getByRole("option", { name: /Main demo/ })).toBeInTheDocument();
    expect(screen.queryByRole("option", { name: /ISA/ })).toBeNull();
    expect(screen.queryByRole("option", { name: /Saxo/ })).toBeNull();
  });
});
