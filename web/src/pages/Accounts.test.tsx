import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { SessionProvider } from "../lib/session";
import { ModeProvider } from "../lib/mode";
import { Accounts } from "./Accounts";

const me = {
  user: { id: "1", username: "admin", role: "admin", disabled: false, mustChangePassword: false, createdAt: "", updatedAt: "" },
  permissions: ["view", "agent.control", "settings.write"], via: "session", csrfToken: "c",
};
const acct = (id: string, name: string, platform: string, mode = "demo") => ({
  id, name, ownerId: "1", ownerName: "admin", mode, platform, envKeys: false, access: "owner", createdAt: "2026-11-23T09:00:00Z",
});
const spec = (key: string, label: string, extra: Record<string, boolean> = {}) => ({
  key, label, help: `${label} help`, multiline: false, optional: false, setByFlow: false, ...extra,
});
const platforms = [
  { platform: "etoro", label: "eToro", viewOnly: false, connectable: false, credentials: [spec("api_key", "Public API key"), spec("user_key", "User key")] },
  { platform: "trading212", label: "Trading 212", viewOnly: true, connectable: false, credentials: [spec("api_key", "API key"), spec("api_secret", "API secret")] },
  { platform: "saxo", label: "Saxo", viewOnly: true, connectable: true, credentials: [
    spec("app_key", "App key"), spec("app_secret", "App secret"), spec("refresh_token", "Refresh token", { setByFlow: true, optional: true }),
    spec("dev_token", "24-hour token (SIM only)", { optional: true }),
  ] },
  { platform: "tiger", label: "Tiger Brokers", viewOnly: true, connectable: false, credentials: [
    spec("tiger_id", "Tiger ID"), spec("account", "Account number"), spec("private_key", "RSA private key (PEM)", { multiline: true }),
    spec("license", "Licence", { optional: true }),
  ] },
];
const keysFor = (url: string) => {
  if (url.includes("/s1/")) {
    return {
      apiKeySet: true, userKeySet: true, fromEnvironment: false, complete: false, connectable: true,
      connectRedirectUri: "https://trader.example:8453/api/v1/oauth/saxo/callback",
      credentials: [
        { ...platforms[2].credentials[0], set: true, updatedAt: "2026-11-23T09:00:00Z" },
        { ...platforms[2].credentials[1], set: true }, { ...platforms[2].credentials[2], set: false }, { ...platforms[2].credentials[3], set: false },
      ],
    };
  }
  return { apiKeySet: true, userKeySet: false, fromEnvironment: false, complete: false, connectable: false, credentials: [] };
};

type Call = { url: string; method: string; body: unknown };

function mockApi(list: unknown[], created: unknown) {
  const calls: Call[] = [];
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
    const url = String(input);
    const method = init?.method ?? "GET";
    const json = (v: unknown, status = 200) => new Response(JSON.stringify(v), { status });
    calls.push({ url, method, body: init?.body ? JSON.parse(String(init.body)) : undefined });
    if (url.endsWith("/api/v1/auth/me")) return json(me);
    if (url.endsWith("/api/v1/platforms")) return json(platforms);
    if (url.endsWith("/api/v1/accounts") && method === "POST") return json(created, 201);
    if (url.endsWith("/keys") && method === "PUT") return new Response(null, { status: 204 });
    if (url.endsWith("/saxo/connect")) return json({ authorizeUrl: "https://sim.logonvalidation.net/authorize?state=x", redirectUri: "https://trader.example:8453/api/v1/oauth/saxo/callback" });
    if (url.endsWith("/api/v1/accounts")) return json(list);
    if (url.endsWith("/keys")) return json(keysFor(url));
    if (url.endsWith("/shares")) return json([]);
    return new Response("{}", { status: 404 });
  });
  return calls;
}

const page = () => render(<SessionProvider><ModeProvider><Accounts /></ModeProvider></SessionProvider>);

afterEach(() => {
  vi.restoreAllMocks();
  window.history.replaceState(null, "", "/");
});

describe("Accounts", () => {
  it("shows accounts as a grid with icon actions, and the create form follows the platform", async () => {
    const calls = mockApi([acct("e1", "Main demo", "etoro"), acct("t1", "Stocks ISA", "trading212", "real")], acct("n1", "New ISA", "trading212"));
    page();
    expect(screen.getByRole("heading", { name: "Trading Platform Accounts" })).toBeInTheDocument();
    const group = await screen.findByRole("group", { name: "Actions for Stocks ISA" });
    expect(group).toHaveClass("btn-group-vertical");
    expect(screen.getByRole("group", { name: "Actions for Main demo" })).toBeInTheDocument();
    for (const l of ["Test connection of Stocks ISA", "Rename Stocks ISA", "Keys of Stocks ISA", "Share Stocks ISA", "Delete Stocks ISA"]) {
      expect(screen.getByLabelText(l)).toBeInTheDocument();
    }
    expect(screen.getByLabelText("Test connection of Stocks ISA")).toBeDisabled(); // view-only platform
    expect(screen.getByLabelText("Test connection of Main demo")).toBeEnabled();
    // the key toggle opens the platform's own key fields (from the platform list)
    await screen.findByRole("option", { name: "Saxo (view-only)" });
    fireEvent.click(screen.getByLabelText("Keys of Stocks ISA"));
    expect(screen.getByLabelText("API secret")).toBeInTheDocument();
    fireEvent.click(screen.getByLabelText("Keys of Main demo"));
    expect(screen.getByLabelText("User key")).toBeInTheDocument();

    // create form: platform first, labels follow it
    expect(screen.getByLabelText(/Public API key.*optional/)).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Platform"), { target: { value: "trading212" } });
    expect(screen.getByLabelText(/^API key.*optional/)).toBeInTheDocument();
    expect(screen.getByLabelText(/^API secret.*optional/)).toBeInTheDocument();
    expect(screen.getAllByText(/Trading 212 app → Settings → API \(Beta\) → Generate API key/).length).toBeGreaterThanOrEqual(2); // form + open key panel
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "New ISA" } });
    fireEvent.change(screen.getByLabelText(/^API key.*optional/), { target: { value: "k" } });
    fireEvent.change(screen.getByLabelText(/^API secret.*optional/), { target: { value: "s" } });
    fireEvent.click(screen.getByRole("button", { name: "Add" }));
    await waitFor(() => expect(calls.filter((c) => c.method !== "GET")).toHaveLength(2));
    const writes = calls.filter((c) => c.method !== "GET");
    expect(writes[0].body).toEqual({ name: "New ISA", mode: "demo", platform: "trading212" });
    expect(writes[1]).toEqual({ url: "/api/v1/accounts/n1/keys", method: "PUT", body: { values: { api_key: "k", api_secret: "s" } } });
    expect(await screen.findByText("New ISA added.")).toBeInTheDocument();
  });

  it("adds a Tiger account with a multi-line private key", async () => {
    const calls = mockApi([], acct("g1", "Tiger paper", "tiger"));
    page();
    await screen.findByRole("option", { name: "Tiger Brokers (view-only)" });
    fireEvent.change(screen.getByLabelText("Platform"), { target: { value: "tiger" } });
    expect(screen.getByText(/upload the public key in the console/)).toBeInTheDocument();
    expect(screen.getAllByText(/Tiger paper accounts have no Australian\s+market/).length).toBeGreaterThanOrEqual(2); // intro + form help
    const pem = screen.getByLabelText(/RSA private key \(PEM\)/);
    expect(pem.tagName).toBe("TEXTAREA");
    expect(screen.getByLabelText(/^Licence \(optional\)/)).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Tiger paper" } });
    fireEvent.change(screen.getByLabelText(/^Tiger ID/), { target: { value: "2015" } });
    fireEvent.change(pem, { target: { value: "-----BEGIN PRIVATE KEY-----\nAAAA\n-----END PRIVATE KEY-----" } });
    fireEvent.click(screen.getByRole("button", { name: "Add" }));
    await waitFor(() => expect(calls.filter((c) => c.method === "PUT")).toHaveLength(1));
    expect(calls.find((c) => c.method === "PUT")!.body).toEqual({
      values: { tiger_id: "2015", private_key: "-----BEGIN PRIVATE KEY-----\nAAAA\n-----END PRIVATE KEY-----" },
    });
  });

  it("connects a Saxo account and shows the redirect address to register", async () => {
    const assign = vi.fn();
    vi.stubGlobal("location", { ...window.location, assign, search: "", pathname: "/accounts" });
    const calls = mockApi([acct("s1", "Saxo SIM", "saxo")], acct("s1", "Saxo SIM", "saxo"));
    page();
    expect(await screen.findByText("https://trader.example:8453/api/v1/oauth/saxo/callback")).toBeInTheDocument();
    expect(await screen.findByText("not connected yet")).toBeInTheDocument();
    expect(screen.getByLabelText("Test connection of Saxo SIM")).toBeDisabled();
    // the refresh token is the sign-in's: no field for it
    await screen.findByRole("option", { name: "Saxo (view-only)" });
    fireEvent.click(screen.getByLabelText("Keys of Saxo SIM"));
    expect(screen.queryByLabelText(/Refresh token/)).toBeNull();
    expect(screen.getByLabelText(/24-hour token/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Connect to Saxo" }));
    await waitFor(() => expect(assign).toHaveBeenCalledWith("https://sim.logonvalidation.net/authorize?state=x"));
    expect(calls.some((c) => c.url === "/api/v1/accounts/s1/saxo/connect")).toBe(true);
    vi.unstubAllGlobals();
  });

  it("says how the Saxo sign-in went", async () => {
    window.history.replaceState(null, "", "/accounts?connect_error=refused");
    mockApi([], acct("x", "x", "etoro"));
    page();
    expect(await screen.findByText(/Saxo refused the sign-in/)).toBeInTheDocument();
    expect(window.location.search).toBe("");
  });
});
