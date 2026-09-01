# Change log

This file records admitted implementation changes. Release notes remain a
separate artifact governed by the release and operations plan.

## Unreleased

### 2026-09-01 10:45 CDT — Consume and recover initial login attempts

Commit: current commit; hash assigned by Git after commit

Affected files:

- `internal/auth/login_consume.go`
- `internal/auth/login_consume_test.go`
- `docs/CHANGELOG.md`

Explanation:

Added the initial-login callback consumption boundary. It strictly validates
and hashes the fixed callback state, supplies an explicit microsecond-precision
consumption time to the atomic sqlc query, rejects non-login/session-bound rows,
revalidates the stored internal return path, and authenticates the protected
nonce and PKCE verifier. Any failure after the database update leaves the
attempt consumed and returns no partial material.

Verification:

- Exact original context, state hash, consumption time, initial-login metadata,
  protected material, and validated return path
- Nil dependencies, canceled context, malformed/noncanonical state, zero clock,
  missing/replayed attempt, database failure, wrong purpose, unexpected session,
  rejected/empty return path, and corrupt protection envelope
- `consumeInitialLogin` at 100% statement coverage; auth package 96.2%

Risks / non-goals:

- Browser-facing handlers must deliberately collapse these diagnostic errors;
  this internal function preserves causes for controlled logging and tests.
- This unit consumes and recovers an attempt only. Code exchange, ID-token
  verification, identity/session transaction, and cookie rotation remain later
  callback boundaries.

### 2026-09-01 10:38 CDT — Bind validated login creation to persistence

Commit: current commit; hash assigned by Git after commit

Affected files:

- `internal/auth/login_begin.go`
- `internal/auth/login_begin_test.go`
- `docs/implementation-spec.md`
- `docs/CHANGELOG.md`

Explanation:

Added the initial-login creation boundary. It rejects invalid dependencies or
canceled context, validates the raw return target before reading entropy,
normalizes the explicit clock to UTC PostgreSQL microsecond precision,
generates and protects one attempt, and synchronously inserts exact typed sqlc
parameters with a fixed five-minute lifetime. Browser material is returned only
after insertion succeeds.

Verification:

- Exact context, validated return path, purpose, nullable session, state hash,
  ciphertext, creation, and exclusive-expiry parameters
- Nil dependencies, canceled context, validation failure/empty result, zero
  clock, material/protection entropy failures, and insert failure
- Validators run before entropy; failed creation never calls insert or returns
  partial browser material
- `beginInitialLogin` at 100% statement coverage; auth package 95.2%

Risks / non-goals:

- The validator is an explicit authority dependency; this function does not
  independently know the configured base path.
- Go strings containing generated browser material remain live until garbage
  collection even when insertion fails; they are never formatted or logged.

### 2026-09-01 10:34 CDT — Correct unreleased change chronology

Commit: `6862d5ead4807644e9ec81229239eb3d8c78de7d`

Affected files:

- `docs/CHANGELOG.md`

Explanation:

Replaced inferred future heading times with the authoritative local Git commit
times for the eight shell/auth units from `f07eb57` through `92c77db`. Assigned
the now-known atomic-consume commit hash. No implementation or release behavior
changed.

Verification:

- Compared every corrected heading against `git log --date=local`
- Change-log order remains strictly newest-first

Risks / non-goals:

- Heading precision is minutes; commit timestamps retain seconds.

### 2026-09-01 10:30 CDT — Add atomic login-attempt persistence and consumption

Commit: `92c77dba2b6018aa07a24adaf69427ba064bb3c1`

Affected files:

- `internal/store/queries/auth.sql`
- `internal/store/db/auth.sql.go`
- `migrations/schema_integration_test.go`
- `docs/implementation-spec.md`
- `docs/CHANGELOG.md`

Explanation:

Added locally generated sqlc queries to insert protected OIDC login attempts and
atomically consume one current unconsumed state hash with `UPDATE ...
RETURNING`. The consume timestamp is explicit, creation is inclusive, expiry is
exclusive, and missing, future, expired, or replayed attempts all produce the
same no-row result.

Verification:

- Red-before-generation compile failure for both typed query methods
- Deterministic local-only sqlc generation and generated-file drift gate
- PostgreSQL 17.10 two-connection simultaneous consume yields exactly one
  complete correct row and one no-row miss
- Replay, expired, and future attempts all miss; only the winner is marked
  consumed and failed attempts remain unconsumed for cleanup
- Fresh schema integration package at 100% statement coverage and `make verify`

Risks / non-goals:

- These queries provide atomic storage semantics only. The auth service owns
  return-path validation, envelope recovery, attempt lifetime, and browser/
  session outstanding-attempt limits.
- Consumed rows are retained temporarily as replay evidence and removed later
  by bounded idempotent cleanup.

### 2026-09-01 10:22 CDT — Authenticate and recover login-attempt secrets

Commit: `2009847c095e0404f4a567266adf66b78c0c2a17`

Affected files:

- `internal/auth/login_protection.go`
- `internal/auth/login_protection_test.go`
- `internal/auth/login_recovery.go`
- `internal/auth/login_recovery_test.go`
- `docs/CHANGELOG.md`

Explanation:

Added the inverse login-attempt protection boundary. Recovery strictly decodes
the canonical browser state, compares its lookup hash in constant time,
validates both fixed envelope formats, derives field-specific keys, authenticates
the state-hash binding, and returns the nonce and PKCE verifier only after both
open successfully and remain distinct.

Verification:

- Exact protection-to-recovery round trip without formatting live material
- Empty, malformed, short, noncanonical, wrong, and hash-mismatched state
- Short, long, wrong-version, modified, and field-swapped envelopes
- Authenticated noncanonical plaintext and repeated recovered values
- Auth package coverage 94.1%; `recoverLoginMaterial` 95.8%

Risks / non-goals:

- The only uncovered recovery branches are rejection by `aes.NewCipher` for a
  fixed valid 32-byte key and by `cipher.NewGCM` for the standard AES block.
  Both are structurally unreachable under their standard-library contracts.
- Recovery authenticates transient material only. Database one-time
  consumption, expiry, and callback transaction policy remain separate.

### 2026-09-01 10:17 CDT — Protect login-attempt database secrets

Commit: `ca15f844876000c6294c076a1dc1b688dc616f1e`

Affected files:

- `internal/auth/login_protection.go`
- `internal/auth/login_protection_test.go`
- `docs/CHANGELOG.md`

Explanation:

Added fixed versioned AES-256-GCM envelopes for the OIDC nonce and PKCE
verifier. Separate HMAC-SHA-256 keys are derived from the 256-bit browser state
for each field, random 96-bit nonces are stored in each envelope, and the state
hash is authenticated as additional data. The database lookup stores only
SHA-256 of the canonical browser state.

Verification:

- Exact state lookup hash and two deterministic envelope SHA-256 fixtures
- Fixed version/72-byte envelope shape and absence of plaintext substrings
- Invalid, malformed, short, noncanonical, or repeated login material fails
  before reading protection entropy
- Nil, short, and failing nonce readers return zero protected material and
  preserve the entropy failure cause
- Auth package coverage 93.0%; `protectLoginMaterial` 91.5%

Risks / non-goals:

- Database protection is keyed by the short-lived 256-bit state. Disclosure of
  the live browser state compromises that attempt and already defeats its CSRF
  role; no long-lived encryption key or OIDC token is stored.
- Five branches are structurally unreachable under the fixed standard-library
  contracts: AES-256 key rejection, standard AES-GCM construction/overhead
  failure, and their two closure error propagations. Fake cipher injection
  merely to color those lines would weaken the mechanism.

### 2026-09-01 10:10 CDT — Reject repeated OIDC secret blocks

Commit: `c6283bf0a43e56ce55de9ddf9a80f7f76005db71`

Affected files:

- `internal/auth/login_material.go`
- `internal/auth/login_material_test.go`
- `docs/CHANGELOG.md`

Explanation:

Fail closed when the entropy source repeats any 256-bit state, nonce, or PKCE
verifier block. In particular, state must never equal the verifier exposed only
to the token endpoint, or the authorization request would disclose the PKCE
proof.

Verification:

- A 96-byte repeated source returns zero login material and an error
- Generator and auth package remain at 100% statement coverage
- 20 repeated race-enabled auth runs and `make verify`

Risks / non-goals:

- Exact-repeat detection does not attempt to estimate entropy quality. The
  production caller still must use `crypto/rand.Reader`.

### 2026-09-01 10:06 CDT — Generate fail-closed OIDC login material

Commit: `5c11449dda4abf259c672ba7d03724745739070c`

Affected files:

- `internal/auth/login_material.go`
- `internal/auth/login_material_test.go`
- `docs/CHANGELOG.md`

Explanation:

Added one injected-entropy generator for independent 256-bit state, nonce, and
PKCE verifier values. All values use unpadded base64url encoding, matching the
PKCE verifier alphabet and producing 43-byte browser parameters. Entropy
failure returns no partial material, and the mutable source buffer is cleared
before return.

Verification:

- Exact deterministic separation of all three 32-byte entropy blocks
- Base64url decoding proves 256 bits per value and 43-byte encoded length
- Nil, short, and failing readers return zero material and preserve the cause
- Production function and package at 100% statement coverage

Risks / non-goals:

- The generated strings are intentionally live secrets until the login attempt
  is consumed; Go strings cannot be reliably zeroed.
- Database hashing/protection and one-time persistence are separate subsequent
  boundaries.

### 2026-09-01 10:02 CDT — Validate configured internal return paths

Commit: `d214516c473f414e26f5f055c416b0f6b6903575`

Affected files:

- `internal/httpui/url_builder.go`
- `internal/httpui/url_builder_test.go`
- `docs/CHANGELOG.md`

Explanation:

Added the application half of the login return-path boundary. One validated URL
builder now accepts only canonical request URIs within its exact configured
application subtree, including root deployments and alternate nested prefixes.
The validator returns the original bytes only after rejecting external or
network authorities, fragments, traversal/empty segments, encoded separators,
backslashes, decoded controls, noncanonical path/query encoding, and values
outside the database byte bound.

Verification:

- Root, `/bb`, alternate nested, Unicode, route, trailing-slash, and sorted
  query success cases
- External, sibling-prefix, traversal, repeated-separator, encoded separator/
  control/backslash, raw Unicode, noncanonical query, fragment, empty-query,
  malformed escape, zero-builder, and overlong rejection cases
- `ValidateReturnPath` at 100% statement coverage
- 20 repeated race-enabled HTTP UI runs and `make verify`

Risks / non-goals:

- A valid return path conveys navigation only. It grants no identity,
  authorization, or CSRF authority.
- Individual destination handlers still validate their own query values.

### 2026-09-01 09:56 CDT — Remove the base-path literal from login-attempt storage

Commit: `09f9b612ff0eaea0fceaa5739cbb08a1f92f38ec`

Affected files:

- `migrations/000001_identity_and_sessions.sql`
- `migrations/schema_integration_test.go`
- `docs/implementation-spec.md`
- `docs/CHANGELOG.md`

Explanation:

Corrected the unreleased initial login-attempt constraint so it accepts an
internal browser path for root or configured-prefix deployments instead of
hard-coding the temporary `/bb` development prefix. PostgreSQL rejects empty,
relative, network-path, absolute-URL, backslash, fragment, control-character,
and overlong values. The application remains responsible for proving the exact
configured base-path containment and canonical encoding before insertion.

Verification:

- PostgreSQL 17.10 fresh migration accepts `/`, `/bb/`, and an alternate nested
  prefix with a query
- PostgreSQL rejects each unsafe structural class without relying on an HTTP
  handler
- Existing migration/schema and full race/coverage verification

Risks / non-goals:

- This changes an initial migration only because no alpha.1 tag or deployed
  migration ledger exists. Once released, migration bytes are immutable.
- Database structure is not a substitute for application base-path validation;
  that boundary is implemented before login-attempt persistence.

### 2026-09-01 09:47 CDT — Render the responsive base-path-safe public shell

Commit: `f07eb57612aac7995901b3d3e38207ccdaf0c6a4`

Affected files:

- `.node-version`, `.npmrc`, `package.json`, `package-lock.json`
- `go.mod`, `go.sum`, `Makefile`
- `assets/styles/app.css`
- `cmd/forum/main.go`
- `internal/httpui/handler.go`, `internal/httpui/handler_test.go`
- `internal/httpui/render.go`, `internal/httpui/render_test.go`
- `internal/httpui/route_pattern.go`, `internal/httpui/route_pattern_test.go`
- `internal/httpui/shell.templ`, `internal/httpui/shell_templ.go`
- `internal/httpui/static.go`, `internal/httpui/static_test.go`
- `internal/httpui/static/app-1.0.0-alpha.1.css`
- `internal/httpui/static/htmx-2.0.10.min.js`
- `internal/httpui/url_builder.go`, `internal/httpui/url_builder_test.go`
- `internal/httpui/view.go`, `internal/httpui/view_test.go`
- `README.md`, `docs/implementation-spec.md`, `docs/CHANGELOG.md`

Explanation:

Added the compiled Templ public document and fragment shell, responsive
Tailwind layout, keyboard-visible focus treatment, skip link, breadcrumbs,
navigation, canonical URL, custom full/fragment `404`, and immutable embedded
Tailwind and HTMX assets. Chi now owns the internal route table while an
explicit bridge preserves the existing method-qualified `request.Pattern`
observability contract across Chi's request clone. The forum process constructs
all browser URLs from immutable configuration before opening PostgreSQL.
The browser policy wraps the full request-ID/logging/recovery chain so bounded
panic responses retain the same CSP and defensive headers.

Verification:

- Read current templ, Chi, Tailwind, HTMX, npm, and Go tool contracts before
  pinning exact versions
- Red-before-green page-view, renderer, static-handler, router, cookie-path,
  and route-pattern tests
- Root and alternate-prefix full pages, HTMX fragments, history restoration,
  health, CSS, JavaScript, `404`, and standards-compliant default `405`
- Rendered-link scan covering `href`, `src`, `action`, `hx-get`, and `hx-post`
  proves no application URL escapes the configured alternate prefix
- Panic-recovery response retains the exact browser security boundary after
  recovery clears unsafe application-added headers
- Exact CSS and HTMX SHA-256 checks; Tailwind scans only `.templ` sources
- Valid restrictive HTMX JSON; no inline script/style dependency
- `npm audit` reported zero known vulnerabilities across the locked graph
- Touched handwritten HTTP UI functions at 100% statement coverage except
  three structurally unreachable hard-coded asset-path error returns in
  `newPageView`; generated Templ code brings the package aggregate to 80.6%
- Actual forum process connected to PostgreSQL 17.10, served full/fragment/CSS/
  `404` responses, logged `GET /` and the static matched pattern, and stopped
  cleanly on SIGTERM
- `make verify` from a clean generated state

Risks / non-goals:

- The area index is deliberately an honest empty-state shell; database-backed
  reads arrive in A1-06.
- Sign-in remains non-interactive until A1-04. No placeholder authentication
  route or fake session is exposed.
- npm permits integrity-locked remote tarballs because Tailwind publishes its
  WASI fallback that way; package scripts and Git dependencies remain disabled.

### 2026-09-01 09:08 CDT — Make HTMX representation selection cache-safe

Commit: `db5eb33aac5c72bb2e2b12c6b4d6fd7529f2bb1a`

Affected files:

- `internal/httpui/response_mode.go`
- `internal/httpui/response_mode_test.go`
- `docs/implementation-spec.md`
- `docs/CHANGELOG.md`

Explanation:

Added the shared full-page/fragment selector using HTMX's documented exact
`HX-Request: true` contract. History restoration always receives a full page,
and every representation-varying response marks both request headers in
`Vary` without replacing an existing cache key.

Verification:

- Read HTMX 2.0.10's current request-header, history-restoration, response
  handling, and cache variation documentation
- Red-before-green response-mode compile failure
- Ordinary, exact HTMX, noncanonical, history-restore, and explicit non-history
  request cases
- Existing `Vary` value preserved before both representation headers
- `go test -mod=readonly -race -cover ./internal/httpui` at 100% statement
  coverage
- 20 repeated race-enabled HTTP UI package runs
- `make verify`

Risks / non-goals:

- `HX-Request` changes representation only; it conveys no identity,
  authorization, CSRF, or validation authority.
- The page-level HTMX configuration that swaps `422` forms and requests full
  history restoration remains part of the layout unit.

### 2026-09-01 09:01 CDT — Install the fixed browser security boundary

Commit: `143ec1ac64e879fa851b686cabe648ec358687eb`

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
