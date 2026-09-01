# Change log

This file records admitted implementation changes. Release notes remain a
separate artifact governed by the release and operations plan.

## Unreleased

### 2026-09-01 07:14 CDT — Bootstrap the migration history ledger

Commit: current commit; hash assigned by Git after commit

Affected files:

- `internal/migration/bootstrap.go`
- `internal/migration/bootstrap_test.go`
- `internal/migration/bootstrap_integration_test.go`
- `docs/CHANGELOG.md`

Explanation:

Added the idempotent PostgreSQL migration-ledger definition with a positive
bigint primary-key version, non-null name, exact 32-byte digest constraint, and
database-generated application timestamp. The operation accepts pgx
connections or transactions through a private execution contract and requires
the caller to hold the migration advisory lock.

Verification:

- Red-before-green compile failure before `ensureHistoryTable` existed
- Exact statement and nil context/executor/execution-failure unit paths
- Idempotent creation on PostgreSQL 17.10
- Successful row/default round trip and rejection of nonpositive version, null
  name, short digest, and duplicate version
- `go test -mod=readonly -race -cover ./internal/migration` with 100% statement
  coverage
- PostgreSQL integration run with the `integration` build tag and 100%
  migration-package statement coverage
- 20 repeated race-enabled package runs
- `make verify`

Risks / non-goals:

- `CREATE TABLE IF NOT EXISTS` creates but does not attest a pre-existing table
  definition. Schema attestation and advisory-lock ownership remain required in
  the executor coordinator.

### 2026-09-01 07:09 CDT — Bound applied migration history queries

Commit: `cfcf74a849c402274152047d72173f7995415e81`

Affected files:

- `internal/migration/query.go`
- `internal/migration/query_test.go`
- `docs/CHANGELOG.md`

Explanation:

Added the exact ordered PostgreSQL history query. Its row limit is the loaded
release count plus one: enough to prove that the database contains an unknown
version, but unable to scan an inconsistent history without a release-derived
bound. Query execution stays behind a private one-method pgx contract used by
real connections and deterministic tests.

Verification:

- Red-before-green compile failure before `readAppliedMigrations` existed
- Exact SQL, ascending version order, and release-plus-one argument
- Nil context, nil connection, empty/overflowing count, and query failure paths
- `go test -mod=readonly -race -cover ./internal/migration` with 100% statement
  coverage
- 20 repeated race-enabled package runs
- `make verify`

Risks / non-goals:

- The migration state table is created by the executor bootstrap unit. Locking
  and transactional execution remain subsequent units.

### 2026-09-01 07:04 CDT — Read applied migration identities

Commit: `b76bc546b8d411dc1ba90388f981f223fa985342`

Affected files:

- `internal/migration/rows.go`
- `internal/migration/rows_test.go`
- `docs/CHANGELOG.md`

Explanation:

Added the result-set boundary for applied migration identities. It owns and
closes the supplied `pgx.Rows`, copies each driver-owned SHA-256 buffer into an
immutable fixed-size value, rejects malformed digest lengths, and preserves
iteration and scan failures. Query construction and ordering remain outside
this single-function unit.

Verification:

- Red-before-green compile failure before `scanAppliedMigrations` existed
- Exact two-row decode and proof that driver-buffer mutation cannot alter the
  returned digest
- Nil rows, scan failure, iteration failure, and short/long digest failures
- `go test -mod=readonly -race -cover ./internal/migration` with 100% statement
  coverage
- 20 repeated race-enabled package runs
- `make verify`

Risks / non-goals:

- The caller must issue the history query ordered by version. The query,
  advisory lock, and transaction execution remain subsequent units.

### 2026-09-01 06:57 CDT — Reject applied migration drift

Commit: `fc23d6d4151a2c303ebcaae74751d940b70a8565`

Affected files:

- `internal/migration/drift.go`
- `internal/migration/drift_test.go`
- `docs/CHANGELOG.md`

Explanation:

Added the pure migration-history oracle. Applied PostgreSQL rows must be an
exact contiguous prefix of loaded files by version, filename, and SHA-256. A
gap, rename, changed byte sequence, or database version unknown to the release
fails closed. The pending result is a slice of the already loaded file set, so
the check performs no SQL-content copies or per-row allocations.

Verification:

- Red-before-green tests before `pendingMigrations` existed
- Fresh, partially applied, and fully applied histories
- Version gap, rename, changed digest, unknown database version, and empty
  release set failures
- `go test -mod=readonly -race -cover ./internal/migration` with 100% statement
  coverage
- `make verify`

Risks / non-goals:

- This unit compares already loaded state only. Reading applied rows, locking,
  and transaction execution remain the next migration units.

### 2026-09-01 06:52 CDT — Pin SQL generation and migration identity

Commit: `aa342147f525e260c6b5f1616979a9f5d76693bd`

Affected files:

- `go.mod`
- `go.sum`
- `internal/migration/loader.go`
- `internal/migration/loader_test.go`
- `docs/implementation-spec.md`
- `docs/CHANGELOG.md`

Explanation:

Pinned sqlc v1.31.1 as a build-only Go tool after reviewing its version-2
configuration, pgx/v5 generation, migration-directory parsing, local analyzer,
and transaction contracts. Rejected tern v2.4.3 because its single version row
cannot detect edited applied SQL. Added the first project-owned migration unit:
a strict flat loader for contiguous six-digit lowercase filenames, exact
SHA-256 content identities, nonempty SQL, and a one-MiB file ceiling.

Verification:

- Red-before-green loader tests before `loadMigrations` existed
- Boundary tests at one MiB minus one, exactly one MiB, one MiB plus one, and
  four MiB
- Missing, duplicate, malformed, empty, nested, read-failure, metadata-failure,
  and dishonest-filesystem cases
- `go test -mod=readonly -race -cover ./internal/migration` with 100% statement
  coverage
- `go tool sqlc version` reports `v1.31.1`
- Production dependency traversal contains no sqlc compiler packages
- `make verify`

Risks / non-goals:

- The sqlc compiler has a large build-only dependency graph and an expensive
  first compilation. It is not linked into the forum binary.
- This unit loads and identifies migrations only. PostgreSQL locking, drift
  comparison, transactional execution, schema SQL, and the migration command
  remain the next A1-02 units.

### 2026-09-01 06:39 CDT — Wire executable PostgreSQL ownership

Commit: `30dc5a96c190ec0b859b64cca160bfd193f252f8`

Affected files:

- `cmd/forum/main.go`
- `cmd/forum/main_test.go`
- `docs/implementation-spec.md`
- `docs/CHANGELOG.md`

Explanation:

Wired the admitted pgx configuration and pool lifecycle into executable
startup. The service now refuses to bind its HTTP listener until PostgreSQL has
passed the bounded initial round trip. Once opened, the pool is closed exactly
once after graceful service stop and on every later startup or listener
failure. The single injected pool-factory seam keeps unit tests deterministic;
its returned errors are redacted, and inconsistent pool-plus-error or nil-pool
results fail closed without leaking ownership.

Verification:

- Red-before-green executable tests before the pool dependency existed
- Pool open failure, returned-pool cleanup, nil result, cancellation during and
  after open, listener failure, and graceful shutdown ownership tests
- `go test -mod=readonly -race -cover ./cmd/forum` with 91.3% statement coverage
  of the testable `run` function; the process-terminating `main` wrapper remains
  deliberately outside in-process coverage
- `make verify`

Risks / non-goals:

- The live pool is not yet passed to repositories because migrations and
  queries do not exist.
- Readiness remains deliberately false until exact migration-head and
  governance singleton checks are wired.

### 2026-09-01 06:32 CDT — Prove PostgreSQL pool startup

Commit: `6b02472fc96dc8ec33bc389722767a0f5d5b8cd6`

Affected files:

- `internal/store/pool.go`
- `internal/store/pool_test.go`
- `internal/store/pool_integration_test.go`
- `docs/implementation-spec.md`
- `docs/release-operations.md`
- `docs/CHANGELOG.md`

Explanation:

Added the PostgreSQL pool ownership boundary. It accepts only a configuration
created through pgx's parser, rejects missing or canceled dependencies, proves
one initial database round trip within five seconds, closes the pool on every
failed startup check, and returns ownership only after success. PostgreSQL 17
is the alpha support target, with PostgreSQL 17.10 and its exact container
digest recorded as the integration reference.

Verification:

- Red-before-green dependency tests before `OpenPool` existed
- pgx v5.10.0 `NewWithConfig`, `Ping`, `Close`, and pool source inspected
- `go test -mod=readonly -race -cover ./internal/store`
- PostgreSQL 17.10 integration tests for successful connection/version proof,
  connection-failure redaction, invalid configuration, and cancellation with
  100% statement coverage of `internal/store`
- `make verify`

Risks / non-goals:

- The executable does not own this pool yet; startup/shutdown wiring is the
  next production unit.
- Migration head, schema invariants, governance cardinality, and readiness
  remain deliberately unimplemented and fail closed.

### 2026-09-01 06:18 CDT — Bound PostgreSQL pool configuration

Commit: `7d0bbdc2caecb17ed06f7b1bdcec103d9c4a43de`

Affected files:

- `go.mod`
- `go.sum`
- `internal/config/database_pool.go`
- `internal/config/database_pool_test.go`
- `docs/implementation-spec.md`
- `docs/CHANGELOG.md`

Explanation:

Pinned pgx v5.10.0 and added the narrow boundary that parses the redacted
`DATABASE_URL` into a pgx pool configuration. The application overrides every
connection-string pool-size and lifetime control used by alpha with fixed,
bounded values and caps connection establishment at five seconds. A missing
secret fails before pgx can fall back to ambient `PG*` process variables, and
parse failures return a fixed diagnostic that cannot echo the connection
string.

Verification:

- Red-before-green tests for missing-secret rejection and hostile
  connection-string pool overrides
- pgx v5.10.0 `pgxpool.Config`, `ParseConfig`, and `NewWithConfig` contracts and
  source inspected
- `go test -mod=readonly -race -cover ./internal/config` with 100% statement
  coverage
- `make verify`

Risks / non-goals:

- This unit constructs configuration only; it does not open a connection,
  create a pool, check schema compatibility, or make readiness succeed.
- Pool lifecycle and PostgreSQL integration evidence remain the next A1-02
  units.

### 2026-09-01 05:20 CDT — Wire the bounded HTTP executable lifecycle

Commit: `b4e0a7ae9b48b4fbf4c175bfa127a18cea5731c3`

Affected files:

- `cmd/forum/main.go`
- `cmd/forum/main_test.go`
- `internal/app/http_handler.go`
- `internal/app/http_handler_test.go`
- `internal/app/http_server.go`
- `internal/app/http_server_test.go`
- `internal/app/http_lifecycle.go`
- `internal/app/http_lifecycle_test.go`
- `docs/implementation-spec.md`
- `docs/CHANGELOG.md`

Explanation:

Added the forum executable, immutable configuration wiring, JSON logging,
cryptographic request-ID middleware composition, explicit HTTP transport
limits, numeric listener binding, signal cancellation, bounded graceful drain,
and forced-close failure reporting. Readiness remains deliberately fail-closed
until A1-02 installs the PostgreSQL compatibility check. Cancellation observed
before listener ownership transfer aborts startup; cancellation after transfer
uses bounded shutdown. The first termination signal is unregistered before it
cancels the service context, restoring default handling for a second signal.

Verification:

- Red-before-green tests for handler composition, server controls, and every
  lifecycle failure branch
- `internal/app` race tests with 100% statement coverage
- Configuration-error secrecy and executable start/stop/listen-failure tests
- `make verify`

Risks / non-goals:

- The unavoidably process-terminating `main` wrapper is not invoked in-process
  by unit tests; tested service behavior lives in `run` and `internal/app`.
- Startup truth is established by readiness and deployment probes; the process
  does not emit a potentially blocking pre-serve "started" event.
- PostgreSQL, migrations, assets, sessions, OIDC, and forum routes remain later
  alpha units.

### 2026-09-01 04:00 CDT — Add bounded HTTP observability

Commit: `bf1131eab34cc0dacb2a0f769b63114baedb3f89`

Affected files:

- `internal/observability/context.go`
- `internal/observability/context_test.go`
- `internal/observability/request_id.go`
- `internal/observability/request_id_test.go`
- `internal/observability/request_id_middleware.go`
- `internal/observability/request_id_middleware_test.go`
- `internal/observability/response_observer.go`
- `internal/observability/response_observer_test.go`
- `internal/observability/recovery_middleware.go`
- `internal/observability/recovery_middleware_test.go`
- `internal/observability/access_log_middleware.go`
- `internal/observability/access_log_middleware_test.go`
- `docs/architecture.md`
- `docs/implementation-spec.md`
- `docs/CHANGELOG.md`

Explanation:

Added server-generated 128-bit request IDs, request context propagation,
bounded structured completion logs, response-state observation, and sanitized
panic handling. Inbound identifiers and recovered panic values are never
trusted or logged. A panic after response commitment closes through net/http's
documented quiet-abort boundary rather than appending a false error response.
An uncommitted panic discards application-added headers before returning its
bounded error response. A downstream quiet-abort sentinel is propagated
unchanged and logged once as an aborted request without inventing a status.

Verification:

- Focused red-before-green tests for every production function and method
- Attacker-controlled ID, invalid generator, query secrecy, panic secrecy,
  informational-then-final status handling, stale-header removal,
  invalid-status recovery, committed-response, quiet-abort propagation, and
  missing-dependency cases
- `make verify`
- Package statement coverage report

Risks / non-goals:

- Middleware composition into the executable is the next unit.
- Streaming, connection hijacking, authenticated-user attribution, and general
  error classification are outside this foundation slice.

### 2026-09-01 03:45 CDT — Assemble immutable startup configuration

Commit: `9ac37c6928bf095f122dc97b69bdf17e04c9dafa`

Affected files:

- `internal/config/config.go`
- `internal/config/config_test.go`
- `internal/config/secret.go`
- `internal/config/secret_test.go`
- `docs/implementation-spec.md`
- `docs/CHANGELOG.md`

Explanation:

Added the all-or-nothing startup loader and a formatting-safe secret value.
Every required key is read once. Non-database values pass bounded parsers, and
the result is admitted only when production transport, session lifetime, and
confidential-client invariants all hold. The database string is treated as an
opaque nonempty secret until pgx validates it in A1-02. Failed loads return a
zero configuration.

Verification:

- Focused red-before-green tests for secret redaction, template isolation, and
  loading
- Missing, malformed, default, production, and cross-field failure matrices
- `make verify`
- Package statement coverage report

Risks / non-goals:

- Environment acquisition is dependency-injected; executable `os.LookupEnv`
  wiring remains separate.
- PostgreSQL syntax and connection validation remain an explicit A1-02 startup
  gate; this loader checks only that `DATABASE_URL` is present and nonempty.
- Secret-bearing fields and the value type are unexported. Future database and
  OIDC client wiring must not add a general-purpose public reveal method.

### 2026-09-01 03:30 CDT — Close logging and cookie-name configuration

Commit: `4d9703d0ac21c9f97e006a12676d4d45c323c9f4`

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
