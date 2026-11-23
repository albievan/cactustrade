import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { SessionProvider } from "../lib/session";
import { LlmReviewer } from "./LlmReviewer";

const me = {
  user: { id: "1", username: "admin", role: "admin", disabled: false, mustChangePassword: false, createdAt: "", updatedAt: "" },
  permissions: ["view", "agent.control", "settings.write"], via: "session", csrfToken: "c",
};
const llm = { settings: { enabled: true, activeProfileId: "p1" }, defaultAnthropicModel: "claude-opus-5-5", providers: [],
  profiles: [{ id: "p1", name: "Claude", provider: "anthropic", model: "", baseUrl: "", timeoutSeconds: 0, keySet: true },
    { id: "p2", name: "DeepSeek desk", provider: "deepseek", model: "deepseek-chat", baseUrl: "", timeoutSeconds: 0, keySet: false }] };

describe("LlmReviewer", () => {
  it("shows profiles as a grid of cards with icon buttons and makes one the default", async () => {
    const puts: unknown[] = [];
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
      const url = String(input);
      const json = (v: unknown) => new Response(JSON.stringify(v), { status: 200 });
      if (url.endsWith("/api/v1/auth/me")) return json(me);
      if (url.endsWith("/api/v1/llm") && init?.method === "PUT") {
        puts.push(JSON.parse(String(init.body)));
        return json({ ...llm, settings: { enabled: true, activeProfileId: "p2" } });
      }
      if (url.endsWith("/api/v1/llm")) return json(llm);
      return new Response("{}", { status: 404 });
    });
    render(<SessionProvider><LlmReviewer /></SessionProvider>);
    expect(await screen.findByRole("group", { name: "Actions for DeepSeek desk" })).toBeInTheDocument();
    expect(screen.getByRole("group", { name: "Actions for Claude" })).toBeInTheDocument();
    expect(screen.getByLabelText("Make Claude the default")).toBeDisabled();
    expect(screen.getByText("no key")).toBeInTheDocument();
    fireEvent.click(screen.getByLabelText("Make DeepSeek desk the default"));
    await waitFor(() => expect(puts).toEqual([{ enabled: true, activeProfileId: "p2" }]));
  });
});
