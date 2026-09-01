# Change log

This file records admitted implementation changes. Release notes remain a
separate artifact governed by the release and operations plan.

## Unreleased

### 2026-09-01 09:01 CDT — Install the fixed browser security boundary

Commit: current commit; hash assigned by Git after commit

Affected files:

- `internal/httpui/security_headers.go`
- `internal/httpui/security_headers_test.go`
- `internal/httpui/handler.go`
- `internal/httpui/handler_test.go`
- `docs/implementation-spec.md`
- `docs/CHANGELOG.md`

Explanation:

Wrapped the complete application router with one fixed defensive browser
policy. The CSP permits only same-origin scripts, styles, fonts, connections,
and manifests, same-origin plus data images, same-origin form actions, and no
default, base, frame-ancestor, or object sources. The boundary also installs
content-sniffing, framing, referrer, cross-origin isolation, origin-agent, and
permissions headers before any handler writes a response.

Verification:

- Red-before-green policy-constructor compile failure
- Policy visible inside the delegated handler before status output
- Exact fixed value for every admitted defensive header
- HSTS explicitly absent because TLS transport policy is Caddy-owned
- Successful health, failed readiness, unknown route, and method-not-allowed
  responses all retain the CSP
- `go test -mod=readonly -race -cover ./internal/httpui` at 100% statement
  coverage
- 20 repeated race-enabled HTTP UI package runs
- `make verify`

Risks / non-goals:

- The fixed policy intentionally rejects inline script and style. Templates and
  HTMX integration must use versioned same-origin assets.
- HSTS, TLS, and the external `/bb` redirect remain Caddy responsibilities.

### 2026-09-01 08:52 CDT — Bind relative and canonical URLs to one authority

Commit: `9d86882ca2fab692478e26e287fb4bdfece34abc`

Affected files:

- `internal/httpui/url_builder.go`
- `internal/httpui/url_builder_test.go`
- `docs/CHANGELOG.md`

Explanation:

Extended the browser URL builder to require a validated, path-consistent
`PUBLIC_BASE_URL` and `BASE_PATH` pair. It now produces prefix-safe relative
paths, canonical absolute URLs that preserve encoded path-segment boundaries,
and deterministically escaped query strings without consulting request headers.

Verification:

- Red-before-green constructor, absolute-URL, and query-path compile failures
- Root, `/bb`, alternate nested, Unicode, and hostile path-segment cases
- Missing, mismatched, credential-bearing, and query-bearing public authority
  rejection
- Uninitialized public builder fails closed instead of fabricating root URLs
- Deterministically sorted, repeated, and escaped query values
- Corrupted internal builder and ambiguous segment failures
- `go test -mod=readonly -race -cover ./internal/httpui` at 100% statement
  coverage
- 20 repeated race-enabled HTTP UI package runs
- `make verify`

Risks / non-goals:

- Route handlers remain responsible for query-length and pagination bounds.
- The builder creates URLs; it does not authorize whether a caller may disclose
  the resource named by a URL.

### 2026-09-01 08:38 CDT — Add the one-shot migration command

Commit: `beb42175c42f42c4b829eb3c1f0ee9fdc9b2e142`

Affected files:

- `cmd/migrate/main.go`
- `cmd/migrate/main_test.go`
- `internal/config/database_connection_test.go`
- `README.md`
- `docs/CHANGELOG.md`

Explanation:

Added `cmd/migrate` as the visible one-shot schema-maintenance entry point. It
loads only the migration database URL, applies the exact SQL files embedded in
the same source release, owns process-signal cancellation, opens one direct
connection through the project runner, and never starts HTTP, constructs a
pool, or retries an unknown database outcome.

Verification:

- Red-before-green compile failure before the command runner existed
- Exact context, parsed direct connection configuration, and release
  filesystem forwarding
- Nil dependency and cancellation-before-side-effect failures
- Malformed credential-bearing configuration failure without secret exposure
- Failure diagnostics never format a potentially credential-bearing pgx
  configuration
- Migration-runner cause preservation
- Tested `run` function at 100% statement coverage
- 20 repeated race-enabled command-package runs
- Real command execution against a fresh PostgreSQL 17.10 database, exact
  four-migration head, idempotent second execution, and disposable cleanup
- `make verify`

Risks / non-goals:

- Forward migrations are privileged release code. The command intentionally
  has no automatic rollback, fake down migration, pool, or retry path.
- Deployment role grants and secret injection remain deployment-owned.

### 2026-09-01 08:25 CDT — Isolate migration database configuration

Commit: `cd30ce6e92013ad6b133aa57c5cc152ed316c0ea`

Affected files:

- `internal/config/database_connection.go`
- `internal/config/database_connection_test.go`
- `docs/CHANGELOG.md`

Explanation:

Added a migration-specific configuration boundary that reads only
`DATABASE_URL`, accepts the same pool-compatible URL grammar as the service,
returns a copied pgx direct-connection configuration, and pins connection
establishment to five seconds. It does not require HTTP or OIDC settings to run
schema maintenance.

Verification:

- Red-before-green compile failure before `LoadDatabaseConnectionConfig`
  existed
- Valid host, port, database, user, pool-parameter compatibility, and timeout
- Nil lookup, missing/empty value, and malformed secret-bearing URL failures
- Secret absent from every failure string
- `go test -mod=readonly -race -cover ./internal/config` at 100% statement
  coverage
- 20 repeated race-enabled config-package runs
- `make verify`

Risks / non-goals:

- The returned pgx structure necessarily contains credentials and must never be
  formatted or logged. Its sole consumer is the migration command boundary.

### 2026-09-01 08:20 CDT — Bound repository transactions

Commit: `53328c37c56c30874a395738ba905eb9f93ee53f`

Affected files:

- `internal/store/transaction.go`
- `internal/store/transaction_test.go`
- `internal/store/queries/foundation.sql`
- `internal/store/db/foundation.sql.go`
- `migrations/schema_integration_test.go`
- `docs/CHANGELOG.md`

Explanation:

Added `store.WithinTx`, which binds generated queries to one pgx transaction,
commits once, performs bounded cancellation-detached rollback on every earlier
failure, joins cleanup failures, and never retries an unknown commit outcome.
Foundation inserts now use the validated identity timestamp for both creation
and verification columns, avoiding impossible client-time/default-time races.

Verification:

- Red-before-green compile failure before `WithinTx` existed
- Successful action, invalid inputs, begin/nil transaction, callback, commit,
  rollback, joined-failure, and canceled-caller paths
- `WithinTx` itself at 100% statement coverage; store package at 95.6% because
  the pre-existing real pool constructor retains its documented integration
  branches
- PostgreSQL 17.10 transaction creates a user plus external identity atomically
  through generated queries
- PostgreSQL 17.10 callback failure leaves no user row
- Generated query drift regenerated and checked by `make verify`
- 20 repeated race-enabled store-package runs

Risks / non-goals:

- The callback may execute external side effects only if its caller explicitly
  owns that non-transactional failure problem. This wrapper governs PostgreSQL
  work and does not pretend to make networks transactional.

### 2026-09-01 08:14 CDT — Generate typed foundation queries

Commit: `0b1e34fe74dbd0f6ebd30964b44a62a733f606ad`

Affected files:

- `sqlc.yaml`
- `internal/store/queries/foundation.sql`
- `internal/store/db/db.go`
- `internal/store/db/models.go`
- `internal/store/db/foundation.sql.go`
- `migrations/schema_integration_test.go`
- `Makefile`
- `docs/CHANGELOG.md`

Explanation:

Configured pinned sqlc v1.31.1 for local PostgreSQL analysis and pgx/v5
generation. Generated schema models plus the first typed identity, governance,
and active-administrator queries. `make generate` is explicit; `make verify`
regenerates with `--no-remote` and rejects working-tree or untracked output
drift before compiling and testing.

Verification:

- Two consecutive `go tool sqlc generate --no-remote` runs produced identical
  SHA-256 values for every generated file
- Full race-enabled Go test suite
- PostgreSQL 17.10 round trips for governance cardinality, active administrator
  count, and external-identity lookup through generated queries
- Fresh-schema integration test remains at 100% migrations-package coverage
- `make verify`, including generation-drift checks

Risks / non-goals:

- Generated code is committed and never hand-edited. This unit provides typed
  primitives only; transaction ownership and repository policy stay in
  handwritten code.
- Database-backed sqlc analysis and managed/remote generation remain disabled.

### 2026-09-01 08:08 CDT — Create the alpha database schema

Commit: `3707087ffe7237fcb4c19496aebb07442f4f0223`

Affected files:

- `migrations/000001_identity_and_sessions.sql`
- `migrations/000002_groups_and_areas.sql`
- `migrations/000003_topics_posts_and_reads.sql`
- `migrations/000004_reports_and_audit.sql`
- `migrations/files.go`
- `migrations/files_test.go`
- `migrations/schema_integration_test.go`
- `docs/CHANGELOG.md`

Explanation:

Added four contiguous forward-only migrations compiled into the release:
identity/session state; groups/areas; topics/posts/read state; and
reports/moderation audit. Closed values, lengths, timestamps, identities,
singletons, target cardinality, post numbering, and audit structure are
database constraints. Row-locking triggers serialize area/group visibility;
deferred triggers prove topic/post pointers and counters at commit; published
area slugs and post topic/number identities are immutable.

Verification:

- Red-before-green embed API test before `migrations.Files` existed
- Embedded filesystem contains only the four exact ordered SQL files
- Fresh public `migration.Apply` into a disposable PostgreSQL 17.10 database
- Exact four-row migration head and one governance singleton
- Invalid roles, visibility, posting modes, duplicate external identities,
  malformed session hashes, duplicate post numbers, invalid report targets,
  invalid audit actors, and inconsistent topic counters rejected
- Area group mappings require group visibility under row locks; visibility and
  slug drift are rejected
- Topic plus first post and a reply commit with deferred circular integrity;
  partial counter corruption rolls back
- `go test -mod=readonly -race -cover ./migrations` with 100% statement coverage
- PostgreSQL integration coverage for the migrations package at 100%
- `make verify`

Risks / non-goals:

- Runtime/migration roles and grants remain deployment-owned configuration;
  application repositories must still append audit rows in the same mutation
  transaction. These migrations do not create cluster roles.
- Search indexes and later feature schema are added only with reviewed forward
  migrations; no fake down path exists.

### 2026-09-01 07:57 CDT — Expose the migration runner

Commit: `6b7d562d0d94413f3398ebbe6dafb2f9575a4aea`

Affected files:

- `internal/migration/api.go`
- `internal/migration/api_test.go`
- `internal/migration/coordinator_integration_test.go`
- `docs/CHANGELOG.md`

Explanation:

Exposed `migration.Apply` as the only production entry point. It accepts a
configuration produced by `pgx.ParseConfig`, opens one direct connection with
`pgx.ConnectConfig`, delegates to the tested owner/coordinator chain, and never
creates a pool or retries an unknown transaction outcome. The adapter
normalizes pgx's typed nil pointer on failed connection attempts before it
crosses the ownership interface.

Verification:

- Red-before-green compile failure before `Apply` existed
- Parsed pgx configuration with a deterministic injected dial failure
- Nil public context/config rejection
- Local race-enabled package coverage at 99.5%; the only local gap is the
  successful real-connection adapter return
- PostgreSQL 17.10 integration suite now enters through public `Apply`, proving
  the successful adapter, connection ownership, lock, migration, unlock, and
  close path at 100% migration-package statement coverage
- 20 repeated race-enabled package runs
- `make verify`

Risks / non-goals:

- `configured` must come from `pgx.ParseConfig`, matching pgx's documented
  precondition. The migration command wiring and embedded release files remain
  subsequent units.

### 2026-09-01 07:51 CDT — Own the migration connection lifetime

Commit: `d0c6daab823520411da17e2c3af20579fbb4c136`

Affected files:

- `internal/migration/owner.go`
- `internal/migration/owner_test.go`
- `internal/migration/coordinator_test.go`
- `docs/CHANGELOG.md`

Explanation:

Added the private connection owner around the release coordinator. It validates
inputs before connecting, owns any non-nil connection even when the connector
also returns an error, preserves cancellation identity, and always closes on a
five-second cancellation-detached context. Close failure is joined with the
connection or coordinator failure rather than hiding either.

Verification:

- Red-before-green compile failure before `applyWithConnector` existed
- Exact context/config forwarding and cancellation during release work
- Nil/canceled inputs, connect failure, cancellation during connect,
  inconsistent nil connection, coordinator failure, and close failure
- Proof that close begins uncanceled after caller cancellation
- `go test -mod=readonly -race -cover ./internal/migration` with 100% statement
  coverage
- 20 repeated race-enabled package runs
- `make verify`

Risks / non-goals:

- This unit accepts an injected private connector so ownership failures are
  deterministic in unit tests. The next unit is the public wrapper pinned to
  `pgx.ConnectConfig`; callers never select a connector.

### 2026-09-01 07:45 CDT — Coordinate a migration release

Commit: `8997208617d606d672653181ccdcdbb10ce5cb80`

Affected files:

- `internal/migration/coordinator.go`
- `internal/migration/coordinator_test.go`
- `internal/migration/coordinator_integration_test.go`
- `docs/CHANGELOG.md`

Explanation:

Composed the migration primitives around one already-open dedicated
connection. Release files load before lock acquisition; the locked section
bootstraps and attests the ledger, validates the applied prefix, executes the
pending suffix in order, and re-reads exact head before unlock. An already
current release avoids the second attestation, history query, and all
transactions. A changed release re-attests the ledger after privileged SQL and
before trusting final history.

Verification:

- Red-before-green compile failure before `applyRelease` existed
- Fresh/current paths and every validation, lock, bootstrap, attestation,
  history, drift, execution, final-attestation, final-head, and unlock failure
  path
- `go test -mod=readonly -race -cover ./internal/migration` with 100% statement
  coverage and 20 repeated race-enabled runs
- PostgreSQL 17.10 fresh apply, idempotent reapply, changed applied bytes, and
  unknown database-version rejection
- PostgreSQL 17.10 concurrent runner proof: two dedicated sessions both return
  success while exactly two migrations are recorded once
- PostgreSQL integration run with the `integration` build tag and 100%
  migration-package statement coverage
- `make verify`

Risks / non-goals:

- This private coordinator does not open or close the connection. The public
  owner wrapper is the next unit and must close its dedicated connection on
  every exit, including unlock or commit uncertainty.

### 2026-09-01 07:35 CDT — Attest the migration ledger schema

Commit: `76d463df47238edc378a74202fcb96d91f427022`

Affected files:

- `internal/migration/attest.go`
- `internal/migration/attest_test.go`
- `internal/migration/attest_integration_test.go`
- `internal/migration/lock.go`
- `docs/CHANGELOG.md`

Explanation:

Added an exact PostgreSQL 17 catalog attestation for the migration ledger.
Before history is trusted, the runner can now prove the permanent ordinary
table, four ordered column types/nullability/defaults, three named validated
constraints and definitions, no inheritance, no row security, and no external
triggers or rewrite rules. This closes the false assurance left by
`CREATE TABLE IF NOT EXISTS`.

Verification:

- Red-before-green compile failure before `attestHistoryTable` existed
- Exact query plus nil/canceled/query-failure/catalog-mismatch unit paths
- PostgreSQL 17.10 acceptance of the exact created table
- PostgreSQL 17.10 rejection after removing the digest constraint, adding an
  extra column, changing the timestamp default, or enabling row security
- PostgreSQL 17.10 rejection of an insert-suppressing rewrite rule
- `go test -mod=readonly -race -cover ./internal/migration` with 100% statement
  coverage
- PostgreSQL integration run with the `integration` build tag and 100%
  migration-package statement coverage
- 20 repeated race-enabled package runs
- `make verify`

Risks / non-goals:

- The attestation is intentionally pinned to PostgreSQL 17.10 catalog output.
  Supporting another major version requires rerunning and admitting the full
  migration, constraint, concurrency, and readiness evidence.

### 2026-09-01 07:28 CDT — Apply one migration atomically

Commit: `b6e1ceaaf983717ab6c1cd9cc5a568ee9d5e5d51`

Affected files:

- `internal/migration/apply_one.go`
- `internal/migration/apply_one_test.go`
- `internal/migration/apply_one_integration_test.go`
- `docs/implementation-spec.md`
- `docs/CHANGELOG.md`

Explanation:

Added the one-file transactional executor. It revalidates the private loaded
identity and digest, begins a PostgreSQL transaction, executes the exact SQL,
inserts its version/name/SHA-256 record, and commits. Every pre-commit failure
receives a bounded cancellation-detached rollback. Commit failure is reported
as outcome-unknown and explicitly requires ledger inspection before retry.

Verification:

- Red-before-green compile failure before `applyMigration` existed
- Exact SQL/record order and identity arguments
- Invalid context, connection, version, filename, content, and digest
- Begin, SQL, ledger, commit, and rollback failures including joined causes
- PostgreSQL 17.10 proof that successful DDL and its ledger row commit together
- PostgreSQL 17.10 proof that partial multi-statement DDL and DDL followed by a
  duplicate ledger record both roll back without residue
- `go test -mod=readonly -race -cover ./internal/migration` with 100% statement
  coverage
- PostgreSQL integration run with the `integration` build tag and 100%
  migration-package statement coverage
- 20 repeated race-enabled package runs
- `make verify`

Risks / non-goals:

- Migration SQL is trusted reviewed release code and may not contain explicit
  transaction control; the runner cannot sandbox arbitrary privileged SQL.
- No automatic retry occurs after a commit error because its outcome may be
  unknown.

### 2026-09-01 07:20 CDT — Serialize migration sessions

Commit: `3d9f08a477844ce4c5ac83569563a1076783a82f`

Affected files:

- `internal/migration/lock.go`
- `internal/migration/lock_test.go`
- `internal/migration/lock_integration_test.go`
- `docs/CHANGELOG.md`

Explanation:

Added a project-specific PostgreSQL session advisory lock around migration
actions. Acquisition and release use the same connection and fixed signed key.
Release receives a five-second cancellation-detached context, reports false or
failed unlocks, and joins cleanup failure with any action failure. The caller
must close the dedicated connection on every return, eliminating leaked locks
even when the unlock outcome is ambiguous.

Verification:

- Red-before-green compile failure before `withMigrationLock` existed
- Exact acquisition/release SQL and stable key
- Nil/canceled inputs, acquisition failure, action failure, false unlock,
  unlock failure, joined causes, and cancellation-detached cleanup
- PostgreSQL 17.10 proof that a second session cannot take the held key and can
  take it immediately after release
- `go test -mod=readonly -race -cover ./internal/migration` with 100% statement
  coverage
- PostgreSQL integration run with the `integration` build tag and 100%
  migration-package statement coverage
- 20 repeated race-enabled package runs
- `make verify`

Risks / non-goals:

- This unit does not own the connection. The coordinator must use a dedicated
  connection, close it on every exit, and never return a possibly locked
  session to a pool.

### 2026-09-01 07:14 CDT — Bootstrap the migration history ledger

Commit: `25f8d2655803a3f172fdb83cc42d8783a3ba4dee`

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
