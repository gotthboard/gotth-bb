# Change log

This file records admitted implementation changes. Release notes remain a
separate artifact governed by the release and operations plan.

## Unreleased

### 2026-09-01 03:30 CDT — Close logging and cookie-name configuration

Commit: current commit; hash assigned by Git after commit

Affected files:

- `internal/config/log_level.go`
- `internal/config/log_level_test.go`
- `internal/config/session_cookie_name.go`
- `internal/config/session_cookie_name_test.go`
- `docs/implementation-spec.md`
- `docs/CHANGELOG.md`

Explanation:

Added a closed structured-log threshold with an `info` default and an
RFC-cookie-token session name with an application-specific default. Cookie
prefixes with incompatible browser-enforced path or transport semantics are
rejected rather than silently producing unusable sessions.

Verification:

- Focused red-before-green unit tests for both production units
- Go `net/http.Cookie.Valid` contract and source checked
- `make verify`
- Package statement coverage report

Risks / non-goals:

- Cookie attributes are set by the later session HTTP boundary, not this
  parser.
- Logger construction and secret-redaction handlers remain separate work.

### 2026-09-01 03:20 CDT — Validate the exact Authentik issuer

Commit: `4d6fedd85768271a7acc91cc5529d5676cea0f0c`

Affected files:

- `internal/config/oidc_issuer.go`
- `internal/config/oidc_issuer_test.go`
- `docs/implementation-spec.md`
- `docs/CHANGELOG.md`

Explanation:

Added exact OIDC issuer validation using Authentik's recommended per-provider
issuer form. Production requires HTTPS; Authentik global issuer mode,
credentials, queries, fragments, relative URLs, and inputs that would be
normalized are rejected. Diagnostics do not echo malformed issuer values.

Verification:

- Authentik issuer-mode and discovery endpoint documentation checked
- Focused red-before-green unit tests
- `make verify`
- Package statement coverage report

Risks / non-goals:

- Discovery retrieval and metadata comparison are not implemented in this
  unit.
- Authentik global issuer mode remains unsupported until a separate trusted
  provider-specific discovery location is modeled.
- Authentik remains login-only; issuer claims do not grant local authority.

### 2026-09-01 03:15 CDT — Add immutable runtime primitives

Commit: `35297caacc9facbc5b3cc58cfbd61dab0c1ab774`

Affected files:

- `internal/config/environment.go`
- `internal/config/environment_test.go`
- `internal/config/duration.go`
- `internal/config/duration_test.go`
- `internal/config/listen_addr.go`
- `internal/config/listen_addr_test.go`
- `docs/implementation-spec.md`
- `docs/CHANGELOG.md`

Explanation:

Added exact environment selection, positive duration parsing, and numeric
listen-address validation as the next immutable configuration units.
Production binds only to IPv4/IPv6 loopback, preserving Caddy as the public
edge. Malformed raw values are not echoed into diagnostics.

Verification:

- Focused red-before-green unit tests for each production unit
- `make verify`
- Package statement coverage report

Risks / non-goals:

- The aggregate environment loader and executable service lifecycle remain
  incomplete.
- Development/test may bind non-loopback deliberately; production may not.

### 2026-09-01 03:00 CDT — Make the governance lock structural

Commit: `1401728efb30c6fa942f4bd4542f43fd45c96056`

Affected files:

- `docs/implementation-spec.md`
- `docs/verification.md`
- `docs/release-operations.md`
- `docs/CHANGELOG.md`

Explanation:

The governance lock is now a real schema invariant: one boolean primary key
constrained to true, seeded by migration, protected from runtime mutation, and
validated by readiness. This prevents missing or multiple coordination rows
from defeating administrator-continuity serialization.

Verification:

- Schema/cardinality contract review
- Readiness and runtime-privilege failure cases added to verification
- Markdown link and whitespace checks

Risks / non-goals:

- Migration privileges can still repair a damaged row deliberately; runtime
  application privileges cannot.

### 2026-09-01 02:50 CDT — Serialize administrator invariants

Commit: `652b48961dcb5b41464f8676f4bf5902e5b1416d`

Affected files:

- `docs/architecture.md`
- `docs/implementation-spec.md`
- `docs/verification.md`
- `docs/CHANGELOG.md`

Explanation:

Administrator continuity now has a concrete PostgreSQL mechanism: bootstrap
and administrator role/suspension transitions lock one seeded governance row
before checking the active-administrator invariant. “Active” means an
administrator role with no effective suspension. No cached count can drift.

Verification:

- Transaction and concurrency contract consistency review
- Completeness oracle added to the verification plan
- Markdown link and whitespace checks

Risks / non-goals:

- This is coordination state, not a generic distributed lock framework.
- The schema and transaction code remain implementation work in A1-02/A1-08.

### 2026-09-01 02:40 CDT — Preserve administrator continuity

Commit: `ffa8dfacb75a37185e02f521a587ddd24fcc818f`

Affected files:

- `docs/prd.md`
- `docs/architecture.md`
- `docs/implementation-spec.md`
- `docs/verification.md`
- `docs/CHANGELOG.md`

Explanation:

The operator bootstrap grant is admitted only while no active administrator
exists and is serialized against role changes. Local demotion and suspension
transitions must reject any result that leaves zero active administrators.
This closes both persistent operator bypass and accidental governance lockout.

Verification:

- Cross-document role/bootstrap consistency review
- Concurrency and failure-path requirements added to the verification contract
- Markdown link and whitespace checks

Risks / non-goals:

- The mechanism remains unimplemented in this documentation-only correction.
- Emergency recovery after external database damage is an operator incident
  procedure, not a hidden bypass in the normal bootstrap command.

### 2026-09-01 02:25 CDT — Make Authentik authentication-only

Commit: `1de1ac2cfbd99241780ad086023f4d6d032b55b1`

Affected files:

- `docs/prd.md`
- `docs/architecture.md`
- `docs/implementation-spec.md`
- `docs/feature-plan.md`
- `docs/verification.md`
- `docs/release-operations.md`
- `docs/CHANGELOG.md`

Explanation:

Authentik now proves identity only. GOTTH Board owns roles, local groups,
restricted-area membership, suspensions, and moderation. OIDC claims cannot
grant local authority. The first administrator is created through an explicit
audited operator command against an existing issuer/subject identity. This
upstream correction prevents the implementation from building the rejected
Authentik-group authorization model.

Verification:

- Canonical-document terminology scan for removed Authentik-group authority
- Requirement and acceptance-boundary consistency review
- Markdown link and whitespace checks

Risks / non-goals:

- This change does not implement the OIDC or local authorization mechanisms.
- Exact Authentik issuer/client, first administrator subject, and deployment
  values remain runtime owner inputs.

## 2026-09-01 — Alpha foundation begins

- Implementation commit: `8382c8f374ec22d60a90786bb0006e4a06776631`
- Evidence commit: `ac0df0a4d0c2f7966bbad02290262ce038aa0830`
- Review-fix commits: `cf2bebd5c25b67b3ca7e5939a2472218fe9c5e22`,
  `d5e9f5e3e947ed40abc40ab99704a3b9a0484a77`,
  `3c1c784a8ef6c9d13630ba31422e0712c35b75da`
- Branch: `feature/alpha-1-foundation`
- Requirements partially addressed: READ-005, OPS-002, OPS-005
- Delivery item: A1-01, incomplete

Changed:

- Pinned Go 1.26.6 in `go.mod` and `.go-version`.
- Added reproducible `make build`, `make test`, and `make verify` commands.
- Added exact base-path validation and public-base-URL validation, including
  production HTTPS enforcement and rejection of traversal, encoded separators,
  credentials, queries, fragments, malformed escaping, and empty hostnames.
  Malformed URL diagnostics redact the raw configured value.
- Added a browser URL builder that preserves the configured prefix, escapes
  each path segment, and rejects empty or dot-segment ambiguity.
- Added a standard-library HTTP shell with liveness and a deliberately failing
  readiness endpoint until PostgreSQL/schema checks exist.

Verification:

```text
git diff --check
make build
make verify
go test -mod=readonly -coverprofile=/tmp/gotth-bb-all.cover ./...
go tool cover -func=/tmp/gotth-bb-all.cover
```

All commands passed on linux/amd64 with `go1.26.6-X:nodwarf5`. The touched
production functions have 100% statement coverage, including failure paths.
The race detector passed. No external dependencies were selected or fetched.

Performance evidence is not applicable to this bounded startup/health slice:
it makes no throughput or latency claim, and all health output is constant
size. The URL/config parsers are linear in their bounded configuration input.

Still incomplete in A1-01: the full immutable configuration loader, structured
logging, request IDs, panic boundary, PostgreSQL-backed readiness, executable
service lifecycle, Templ/Tailwind asset build, CI, and secret scanning.

Before admission, rollback is deletion of the isolated feature branch and
worktree. After admission by a merge commit, revert that merge commit; do not
rewrite shared history. If these commits are admitted individually, revert
them newest-to-oldest using the exact hashes recorded above.
