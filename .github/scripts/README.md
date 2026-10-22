# Retained automation scripts

Only scripts that still serve the Go/Wails codebase live here. The legacy
Rust/Tauri-era CI, issue-bot, release and Windows installer scripts were
removed with `.github/legacy-workflows/` after the Go migration landed;
they remain available in git history.

- `sign-mcp-macos.mjs`, `test-mcp-signing-macos.sh`, `mcp-signing.md` —
  signing helpers and notes for the macOS MCP helper, kept as the basis
  for the Developer ID signing/notarization step.
- `bump-agent-versions.mjs` (with its test) — version bumps for the Java
  agent drivers under `agents/`, which the Go backend still ships.
- `i18n-autofill.mjs` — locale autofill parser used by
  `packages/app-tests/i18nAutofillParser.test.ts`.
