import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
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

describe("Accounts", () => {
  it("shows accounts as a grid with icon actions, and the create form follows the platform", async () => {
    const posts: { url: string; body: unknown }[] = [];
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
      const url = String(input);
      const json = (v: unknown, status = 200) => new Response(JSON.stringify(v), { status });
      if (init?.method === "POST" || init?.method === "PUT") posts.push({ url, body: JSON.parse(String(init.body)) });
      if (url.endsWith("/api/v1/auth/me")) return json(me);
      if (url.endsWith("/api/v1/accounts") && init?.method === "POST") return json(acct("n1", "New ISA", "trading212"), 201);
      if (url.endsWith("/keys") && init?.method === "PUT") return new Response(null, { status: 204 });
      if (url.endsWith("/api/v1/accounts")) return json([acct("e1", "Main demo", "etoro"), acct("t1", "Stocks ISA", "trading212", "real")]);
      if (url.endsWith("/keys")) return json({ apiKeySet: true, userKeySet: false, fromEnvironment: false });
      if (url.endsWith("/shares")) return json([]);
      return new Response("{}", { status: 404 });
    });
    render(<SessionProvider><ModeProvider><Accounts /></ModeProvider></SessionProvider>);
    expect(screen.getByRole("heading", { name: "Trading Platform Accounts" })).toBeInTheDocument();
    const group = await screen.findByRole("group", { name: "Actions for Stocks ISA" });
    expect(group).toHaveClass("btn-group-vertical");
    expect(screen.getByRole("group", { name: "Actions for Main demo" })).toBeInTheDocument();
    for (const l of ["Test connection of Stocks ISA", "Rename Stocks ISA", "Keys of Stocks ISA", "Share Stocks ISA", "Delete Stocks ISA"]) {
      expect(screen.getByLabelText(l)).toBeInTheDocument();
    }
    expect(screen.getByLabelText("Test connection of Stocks ISA")).toBeDisabled(); // view-only platform
    expect(screen.getByLabelText("Test connection of Main demo")).toBeEnabled();
    // the key toggle opens the platform's own key fields
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
    await waitFor(() => expect(posts).toHaveLength(2));
    expect(posts[0].body).toEqual({ name: "New ISA", mode: "demo", platform: "trading212" });
    expect(posts[1]).toEqual({ url: "/api/v1/accounts/n1/keys", body: { apiKey: "k", userKey: "s" } });
    expect(await screen.findByText("New ISA added.")).toBeInTheDocument();
  });
});
