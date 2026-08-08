// ESLint flat config — v1 frontend.
//
// Enforces:
//   - no `any` (use `unknown` + type guards)
//   - exhaustive React hooks deps (catches stale-closure bugs)
//   - no `console.log/info/debug/warn/error` in committed code
//     (errors that need logging go through the ErrorBoundary, which is the
//     one place allowed to use console.error — see the override below)
//   - no unused vars (TS handles this at build time; eslint is the backup)
//
// Next.js's own lint rules (no-img-element, etc.) are loaded via the
// `next/core-web-vitals` config in `.eslintrc` — but Next 16 dropped
// .eslintrc support in favor of flat config. We pull in the Next plugin
// directly when present.

import js from "@eslint/js";
import tseslint from "typescript-eslint";
import reactHooks from "eslint-plugin-react-hooks";
import globals from "globals";

export default tseslint.config(
  {
    ignores: [
      ".next/**",
      "node_modules/**",
      "out/**",
      "next-env.d.ts",
      "package-lock.json",
    ],
  },
  js.configs.recommended,
  ...tseslint.configs.recommended,
  {
    files: ["**/*.{ts,tsx,js,jsx,mjs}"],
    languageOptions: {
      globals: {
        ...globals.browser,
        ...globals.node,
      },
    },
    plugins: {
      "react-hooks": reactHooks,
    },
    rules: {
      "@typescript-eslint/no-explicit-any": "error",
      "@typescript-eslint/no-unused-vars": [
        "warn",
        { argsIgnorePattern: "^_", varsIgnorePattern: "^_" },
      ],
      "react-hooks/exhaustive-deps": "error",
      "react-hooks/rules-of-hooks": "error",
      "no-console": "error",
      "no-debugger": "error",
      "no-empty": ["error", { allowEmptyCatch: true }],
    },
  },
  // ErrorBoundary is the single sanctioned place to log render errors —
  // there is no Sentry/etc. wired up, so console.error is the only signal
  // a developer has when a section crashes. Override no-console there.
  {
    files: ["src/components/sentinel/error-boundary.tsx"],
    rules: {
      "no-console": "off",
    },
  },
  // Vitest setup files reference the global `vi` helper.
  {
    files: ["src/__tests__/**", "vitest.config.ts"],
    rules: {
      "no-console": "off",
    },
  },
);
