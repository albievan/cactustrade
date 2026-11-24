import js from "@eslint/js";
import globals from "globals";
import reactHooks from "eslint-plugin-react-hooks";
import tseslint from "typescript-eslint";

// The browser's blocking dialogs are banned (PLAN 6.15): ask with components/ConfirmDialog.
const dialogs = ["confirm", "alert", "prompt"];
const why = "use components/ConfirmDialog instead of the browser's blocking dialogs";

export default tseslint.config(
  { ignores: ["dist", "node_modules", "src/lib/schema.d.ts"] },
  {
    extends: [js.configs.recommended, ...tseslint.configs.recommended],
    files: ["**/*.{ts,tsx}"],
    languageOptions: { ecmaVersion: 2022, globals: globals.browser },
    plugins: { "react-hooks": reactHooks },
    rules: {
      ...reactHooks.configs.recommended.rules,
      "no-restricted-properties": ["error",
        ...["window", "globalThis", "self"].flatMap((object) => dialogs.map((property) => ({ object, property, message: why })))],
      "no-restricted-globals": ["error", ...dialogs.map((name) => ({ name, message: why }))],
    },
  },
);
