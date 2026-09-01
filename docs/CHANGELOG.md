# Change log

This file records admitted implementation changes. Release notes remain a
separate artifact governed by the release and operations plan.

## Unreleased

### 2026-09-01 02:25 CDT — Make Authentik authentication-only

Commit: current commit; hash assigned by Git after commit

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
