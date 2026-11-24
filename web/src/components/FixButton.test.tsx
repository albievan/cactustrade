import { fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";
import { FixButton, fixes } from "./FixButton";
import type { Missing } from "../lib/api";

const inRouter = (ui: React.ReactNode) => render(<MemoryRouter>{ui}</MemoryRouter>);

describe("FixButton", () => {
  it.each<[Missing, string, string]>([
    ["trading_budget", "Set budget", "/risk"],
    ["account_keys", "Add keys", "/accounts?keys=a%201"],
    ["account", "Add account", "/accounts"],
    ["watchlist", "Add shares", "/watchlist"],
    ["reconcile", "Reconcile", "/"],
    ["kill_reset", "Reset kill switch", "/"],
    ["llm_profile", "Fix reviewer", "/llm"],
    ["email_server", "Set up e-mail", "/email"],
  ])("%s is a compact link “%s” to %s", (what, label, href) => {
    inRouter(<FixButton what={what} accountId="a 1" />);
    const link = screen.getByRole("link");
    expect(link).toHaveTextContent(label);
    expect(link).toHaveAttribute("href", href);
    expect(link).toHaveAttribute("title", fixes[what].title);
    expect(link.getAttribute("aria-label")).toMatch(new RegExp("^" + label));
    expect(link).toHaveClass("btn", "btn-sm");
    expect(link.querySelector("i.bi")).not.toBeNull();
  });

  it("links to Platform accounts without an account id", () => {
    inRouter(<FixButton what="account_keys" />);
    expect(screen.getByRole("link")).toHaveAttribute("href", "/accounts");
  });

  it("runs the page's own fix instead of linking", () => {
    const onFix = vi.fn();
    inRouter(<FixButton what="reconcile" onFix={onFix} />);
    expect(screen.queryByRole("link")).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /^Reconcile/ }));
    expect(onFix).toHaveBeenCalledOnce();
  });

  it("can be disabled while the page is busy", () => {
    inRouter(<FixButton what="kill_reset" onFix={() => undefined} disabled />);
    expect(screen.getByRole("button", { name: /^Reset kill switch/ })).toBeDisabled();
  });

  it("explains CACTUSTRADE_PUBLIC_URL in a tooltip, with nothing to press", () => {
    inRouter(<FixButton what="public_url" onFix={() => undefined} />);
    expect(screen.queryByRole("link")).not.toBeInTheDocument();
    expect(screen.queryByRole("button")).not.toBeInTheDocument();
    const help = screen.getByRole("note");
    expect(help.getAttribute("title")).toMatch(/CACTUSTRADE_PUBLIC_URL/);
    expect(help).toHaveTextContent("Public URL");
  });

  it("has a fix for every code", () => {
    for (const f of Object.values(fixes)) {
      expect(f.label.length).toBeGreaterThan(0);
      expect(f.missing.length).toBeGreaterThan(0);
      expect(!!f.to !== !!f.help).toBe(true);
    }
  });
});
