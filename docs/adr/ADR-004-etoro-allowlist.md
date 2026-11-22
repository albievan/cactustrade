# ADR-004: The eToro client is an allowlist

**Status:** accepted (2026-11-22)

**Context.** The owner's keys may carry more permissions than trading needs (transfers, copy
trading…). The brief's first guardrail is that the agent can never add, request, move or
withdraw money.

**Decision.** `internal/adapters/etoro/allowlist.go` lists every operation the client may
perform, each with a demo path and a real path. eToro does not put the `demo` segment in the
same place for every endpoint, so a path rewrite would be wrong. `allowlist_test.go`:

- parses every non-test Go file of the package and fails on any `/api/` string literal that
  is not an exact allowlisted template;
- fails on any allowlisted path containing a money, balance, transfer, cash, sub-account,
  copy, agent-portfolio, SSO, watchlist or social fragment;
- pins the order constants: `buy`, leverage 1, `settlementType: real`, `mkt`, `usd`;
- checks that an unknown mode resolves to the demo path.

`make etoro-spec` re-downloads eToro's OpenAPI spec and reports allowlisted paths that no
longer exist there.

**Consequences.** Adding an endpoint means editing the allowlist, which shows up plainly in
review (CLAUDE.md §2.1).
