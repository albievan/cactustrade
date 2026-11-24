import { fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";
import { ProblemAlert } from "./ProblemAlert";
import { ApiError, type Problem } from "../lib/api";

const inRouter = (ui: React.ReactNode) => render(<MemoryRouter>{ui}</MemoryRouter>);

const budget: Problem = {
  title: "Cannot do that now", status: 422, field: "tradingBudgetCents",
  detail: "Set a trading budget before starting the agent", missing: "trading_budget", canFix: true,
};

describe("ProblemAlert", () => {
  it("renders nothing without an error", () => {
    const { container } = inRouter(<><ProblemAlert error={null} /><ProblemAlert error="" /></>);
    expect(container).toBeEmptyDOMElement();
  });

  it("renders the fix button from a 422 problem", () => {
    inRouter(<ProblemAlert error={new ApiError(422, budget)} />);
    const alert = screen.getByRole("alert");
    expect(alert).toHaveClass("alert-danger");
    expect(alert).toHaveTextContent("Set a trading budget before starting the agent");
    expect(screen.getByRole("link", { name: /^Set budget/ })).toHaveAttribute("href", "/risk");
  });

  it("shows no button when the caller may not fix it", () => {
    inRouter(<ProblemAlert error={new ApiError(409, { title: "Conflict", status: 409, detail: "The kill switch is on", missing: "kill_reset", canFix: false })} />);
    expect(screen.getByRole("alert")).toHaveTextContent("The kill switch is on");
    expect(screen.queryByRole("link")).not.toBeInTheDocument();
    expect(screen.queryByRole("button")).not.toBeInTheDocument();
  });

  it("always explains a deployment setting", () => {
    inRouter(<ProblemAlert error={new ApiError(503, { title: "Unavailable", status: 503, detail: "Set CACTUSTRADE_PUBLIC_URL first", missing: "public_url", canFix: false })} />);
    expect(screen.getByRole("note").getAttribute("title")).toMatch(/CACTUSTRADE_PUBLIC_URL/);
  });

  it("uses the page's own fix when it has one", () => {
    const fix = vi.fn();
    inRouter(<ProblemAlert error={new ApiError(400, { title: "Invalid input", status: 400, detail: "No mail server", missing: "email_server", canFix: true })}
      fixes={{ email_server: fix }} />);
    fireEvent.click(screen.getByRole("button", { name: /^Set up e-mail/ }));
    expect(fix).toHaveBeenCalledOnce();
  });

  it("passes the account to the keys link", () => {
    inRouter(<ProblemAlert error={new ApiError(503, { title: "Unavailable", status: 503, detail: "The account's eToro keys are not set", missing: "account_keys", canFix: true })}
      accountId="a1" />);
    expect(screen.getByRole("link", { name: /^Add keys/ })).toHaveAttribute("href", "/accounts?keys=a1");
  });

  it("shows a plain error or message without a button", () => {
    inRouter(<><ProblemAlert error={new Error("boom")} /><ProblemAlert error="the passwords differ" variant="warning" /></>);
    expect(screen.getByText("boom")).toBeInTheDocument();
    expect(screen.getByText("the passwords differ").closest(".alert")).toHaveClass("alert-warning");
    expect(screen.queryByRole("link")).not.toBeInTheDocument();
  });
});
