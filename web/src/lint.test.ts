// @vitest-environment node
import { ESLint } from "eslint";
import { describe, expect, it } from "vitest";

// PLAN 6.15: the browser's blocking dialogs fail the lint (ask with ConfirmDialog instead).
describe("eslint bans window.confirm, alert and prompt", () => {
  const eslint = new ESLint(); // the project's eslint.config.js (pnpm runs the tests in web/)
  const rules = new Set(["no-restricted-properties", "no-restricted-globals"]);
  it.each([
    "window.confirm('Delete?')",
    "window.alert('Done')",
    "window.prompt('Password')",
    "globalThis.confirm('Delete?')",
    "self.alert('Done')",
    "confirm('Delete?')",
    "alert('Done')",
    "prompt('Password')",
  ])("a planted %s fails", async (code) => {
    const [r] = await eslint.lintText(`export const x = () => ${code};\n`, { filePath: "src/pages/Planted.tsx" });
    expect(r.messages.filter((m) => m.ruleId && rules.has(m.ruleId))).toHaveLength(1);
    expect(r.errorCount).toBeGreaterThan(0);
  });

  it("allows other window calls", async () => {
    const [r] = await eslint.lintText("export const x = () => window.location.reload();\n", { filePath: "src/pages/Planted.tsx" });
    expect(r.errorCount).toBe(0);
  });
});
