# ADR-006: Sign-in, roles, API keys and the default administrator

**Status:** accepted (2026-11-22)

**Decision.**
- argon2id passwords (64 MiB, t=3, p=2), at least 12 characters.
- Server-side sessions: a random token in an HttpOnly, Secure, SameSite=Strict cookie, with
  only its SHA-256 stored. Limits are 12 h absolute and 1 h idle.
- CSRF: a per-session token sent in `X-CSRF-Token` on every cookie-authenticated mutation,
  plus a check of the `Origin` header.
- Roles admin ⊃ operator ⊃ viewer, with permissions defined in `internal/domain/roles.go`.
  Every use case checks its permission. Handlers and React only hide or show things.
- API keys are `ctk_<id>_<secret>` bearer tokens, stored hashed. A key's role is capped by its
  owner's role. Keys cannot create keys or change credentials.
- First start (no users): the server creates `admin` / `admin` flagged *must change password*
  (owner's choice, 2026-11-23; this replaced a one-time setup token). Until the password is
  changed, the account (and any API key it owns) can only read `/auth/me`, change its
  password or sign out; everything else is 403 "Password change required". Creation is
  atomic across replicas (`CreateFirst`).
- Sign-in is rate-limited per IP and per username.

**Consequences.** The last enabled admin cannot be deleted, disabled or demoted.
