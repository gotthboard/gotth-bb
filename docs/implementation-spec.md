# Implementation specification

## Document control

| Field | Value |
| --- | --- |
| Status | Draft constrained by PRD and architecture |
| Applies to | Version 1.0 implementation |
| Product contract | [Product requirements](prd.md) |
| System contract | [Architecture](architecture.md) |

## 1. Implementation posture

Implement the smallest direct mechanism satisfying the version 1.0
requirements. Do not create plugin systems, generic entity repositories,
distributed-service interfaces, or a client-side application shell.

The implementation must preserve these invariants:

1. Authentik is the only authentication authority.
2. Issuer plus subject is the only external identity key.
3. Restricted rows are filtered in SQL before return, count, rank, or render.
4. Every browser mutation is authorized and CSRF-protected server-side.
5. Every access-changing or moderation mutation commits with its audit event.
6. Every browser-facing URL is generated through the configured base path.
7. PostgreSQL constraints and transactions remain authoritative.

## 2. Toolchain and dependencies

The initial implementation shall use:

- Go with the version pinned in `go.mod` and the CI/toolchain configuration.
- `net/http` with Chi version `v5.3.2`; the custom not-found hook, default
  method-not-allowed/`Allow` behavior, route context, and standard middleware
  interface are verified against that release's documentation and source.
- Templ version `v0.3.1020`, pinned as a Go tool and runtime dependency, for
  compiled server-side components.
- HTMX version `2.0.10`, copied from the exact npm lock and served as a
  versioned same-origin embedded asset.
- Tailwind CSS and `@tailwindcss/cli` version `4.3.3`, pinned through npm
  `12.0.2` on Node.js `26.7.0` and run at build time. Automatic source
  detection is disabled; only reviewed `.templ` files supply utility classes.
- `pgx/v5` version `v5.10.0` for PostgreSQL access.
- PostgreSQL 17 for alpha, with PostgreSQL 17.10 as the pinned integration
  reference. Other major versions are unsupported until the same migration,
  constraint, concurrency, and readiness evidence is run against them.
- `sqlc` version `v1.31.1`, pinned as a Go tool, for typed query generation
  from reviewed SQL. Generation uses the local PostgreSQL analyzer and
  `pgx/v5`; managed databases and remote analysis are disabled.
- A project-owned forward migration runner. Migration files use contiguous
  six-digit versions, lowercase names, a one-MiB per-file limit, and immutable
  SHA-256 records in PostgreSQL. The runner takes a PostgreSQL advisory lock,
  verifies every applied name and digest before executing pending SQL, and
  applies each migration and its record in one transaction. There is no fake
  `down` path for destructive schema changes.
- `go-oidc/v3` and `x/oauth2` or an equivalent documented OIDC implementation.
- Goldmark for Markdown parsing and Bluemonday or an equivalent explicit HTML
  allowlist sanitizer.

Exact versions are pinned at implementation start, after reading their current
manuals and release notes. No dependency is selected solely because it is
fashionable or familiar.

## 3. Repository layout

```text
cmd/
  forum/                 service entry point
  migrate/               migration entry point, if not a forum subcommand
internal/
  app/                   startup wiring and lifecycle
  auth/                  OIDC and session use cases
  access/                explicit read/write policy functions
  forum/                 area, topic, post use cases
  moderation/            reports and moderation transitions
  admin/                 area and account administration
  store/                 pgx pool, transactions, sqlc output, repositories
  httpui/                routes, middleware, handlers, view models
  render/                Markdown rendering and sanitization
  config/                environment parsing and validation
  observability/         logging, request IDs, health checks
web/
  templates/             Templ components
  assets/                source assets
  static/                generated/versioned static output
db/
  migrations/            ordered schema migrations
  queries/               sqlc SQL grouped by use case
docs/                    canonical project documents
tests/
  integration/           PostgreSQL and HTTP integration tests
  fixtures/              non-secret deterministic fixtures
```

Generated files are identified and reproducible. Generated code is either
committed consistently or produced in CI consistently; the project shall not
mix both policies.

## 4. Configuration contract

Configuration is loaded once at startup, validated, then treated as immutable.
Unknown or malformed security-sensitive settings fail startup.
Required settings distinguish a missing key from a deliberately empty value;
the root deployment therefore supplies `BASE_PATH` as an explicit empty value.

| Setting | Required | Purpose |
| --- | --- | --- |
| `APP_ENV` | Yes | `development`, `test`, or `production` |
| `LISTEN_ADDR` | Yes | Numeric IP and nonzero port; loopback in production |
| `PUBLIC_BASE_URL` | Yes | Exact external base, including any path prefix |
| `BASE_PATH` | Yes | Browser path prefix; explicitly empty for the initial deployment |
| `DATABASE_URL` | Yes | Opaque PostgreSQL connection string supplied as a secret |
| `OIDC_ISSUER_URL` | Yes | Exact Authentik issuer |
| `OIDC_CLIENT_ID` | Yes | OIDC client identifier |
| `OIDC_CLIENT_SECRET` | Yes in production | Confidential-client secret |
| `BOOTSTRAP_ADMIN_SUBJECT` | Yes | Exact verified OIDC subject allowed to claim first-run administration |
| `REGISTRATION_URL` | Yes | Exact same-origin Authentik enrollment-flow URL |
| `REGISTRATION_ENABLED` | Yes | Exact `true`/`false` operational gate for the public registration route and link |
| `SESSION_COOKIE_NAME` | No | Defaults to `gotth_bb_session` |
| `SESSION_MAX_AGE` | Yes | Absolute authenticated-session lifetime |
| `SESSION_IDLE_TIMEOUT` | Yes | Idle session expiry |
| `AUTH_REVALIDATE_INTERVAL` | Yes | Maximum accepted Authentik identity staleness |
| `LOG_LEVEL` | No | `debug`, `info`, `warn`, or `error`; defaults to `info` |

Rules:

- `PUBLIC_BASE_URL` must be HTTPS in production, contain no query or fragment,
  and its path must equal normalized `BASE_PATH`.
- `BASE_PATH` is empty or begins with one `/`, has no trailing slash, and
  contains no traversal or encoded separator.
- `LISTEN_ADDR` must be an explicit numeric IP/port. Production accepts only
  IPv4 or IPv6 loopback so the service cannot bypass the Caddy edge boundary.
- The configuration loader requires a nonempty `DATABASE_URL` without parsing
  or logging it. A1-02 passes it directly to pgx's documented configuration
  parser; driver rejection aborts startup before readiness or request serving.
  The application then overrides connection-string pool controls with its
  bounded alpha policy: at most 10 connections, no prewarmed connections, a
  5-second connect timeout, a 30-minute connection lifetime, a 5-minute idle
  lifetime, a 30-second health-check period, and a 2-second ping timeout.
- `SESSION_MAX_AGE`, `SESSION_IDLE_TIMEOUT`, and `AUTH_REVALIDATE_INTERVAL`
  must use Go duration syntax such as `30m` or `24h` and be at least one
  second; sub-second, zero, and negative durations fail startup. Idle timeout
  and Authentik revalidation interval may not exceed the absolute session
  maximum. `SESSION_MAX_AGE` must
  be at least one second because browser cookie expiry has whole-second
  precision; a shorter server session cannot produce a reliably live cookie.
- `SESSION_COOKIE_NAME` must be a valid HTTP cookie token. Browser magic
  prefixes (`__Host-`, `__Secure-`, `__Http-`, and `__Host-Http-`) are rejected
  case-insensitively because their transport and path requirements conflict
  with supported root and path-prefixed deployment semantics.
- `OIDC_ISSUER_URL` must be the exact Authentik per-provider issuer in
  `/application/o/<application_slug>/` form: an absolute HTTP(S) URL with no
  credentials, query, or fragment. Production requires HTTPS. Encoding and
  the required trailing slash are identity-significant and are not normalized
  by the application. The slug uses Authentik's ASCII letters, digits, hyphen,
  and underscore grammar without percent encoding, and may not be one of
  Authentik's documented reserved OAuth endpoint slugs. Authentik global
  issuer mode is unsupported because its discovery document remains at a
  separate provider-specific path.
- OIDC callback is computed as `PUBLIC_BASE_URL + /auth/callback`; it is not a
  separate free-form setting.
- `BOOTSTRAP_ADMIN_SUBJECT` is 1 through 512 valid UTF-8 characters without
  controls. It is compared only with the verified subject stored for the
  current local account and is never rendered, logged, or accepted from a
  browser.
- `REGISTRATION_URL` must share the exact scheme and authority of
  `OIDC_ISSUER_URL`, use HTTPS in production, have the canonical
  `/if/flow/<flow_slug>/` path, and contain no credentials, query, or fragment.
  The registration handler appends the application-owned absolute `/login`
  return URL; browser input cannot select the enrollment authority or return.
- `REGISTRATION_ENABLED` accepts only lowercase `true` or `false`. It remains
  `false` until first-administrator setup, enrollment blueprint application,
  email delivery verification, and the sibling-application access audit all
  pass. While false, `/register` is not routed and no Register link is shown.
- OIDC claims never assign forum roles or local group membership.
- `OIDC_CLIENT_SECRET` is required in production and may be absent only for a
  non-production public-client test setup.
- Database and OIDC client secrets use an unexported redacting value type with
  no general-purpose reveal method. PostgreSQL pool parsing and OIDC service
  construction receive them through narrow boundary-specific methods. The
  OIDC method revalidates the immutable public/issuer configuration, computes
  the exact callback, and passes the client secret only into the concrete
  authentication service. The secrets are
  not available to templates, ordinary formatting, logs, diagnostics, or
  health output.

## 5. Core types

### 5.1 Identity and role

```go
type Role uint8

const (
    RoleMember Role = iota + 1
    RoleModerator
    RoleAdministrator
)

type AccessContext struct {
    Authenticated bool
    UserID        int64
    Role          Role
    GroupIDs      []int64
    Suspended     bool
    MutedUntil    *time.Time
    ValidatedAt   time.Time
}
```

Anonymous context uses `Authenticated=false` and no synthetic user ID. Role
ordering may support explicit comparisons, but callers shall use named methods
instead of magic numeric checks.
`AccessContext.Valid` is the single structural authority check before a snapshot
is translated into repository facts: anonymous state has no user, role, or
groups; authenticated state has a positive local user, one known local role,
and only positive local group IDs. Suspension, mute, and validation time remain
operation-specific facts rather than structural invalidity.

### 5.2 Area policy

```go
type Visibility string

const (
    VisibilityPublic        Visibility = "public"
    VisibilityAuthenticated Visibility = "authenticated"
    VisibilityGroups        Visibility = "groups"
)

type PostingMode string

const (
    PostingNormal   PostingMode = "normal"
    PostingReadOnly PostingMode = "read_only"
    PostingArchived PostingMode = "archived"
)
```

Database check constraints contain the same closed values. Unknown stored
values are errors, not a default policy.
The in-memory view predicate also rejects contradictory anonymous/authenticated
authority, nonpositive group IDs, and group mappings on non-group visibility.
Suspension and mute state do not change visibility; publishing predicates apply
those restrictions separately. Read repositories still repeat the equivalent
predicate inside SQL and do not authorize by filtering fetched rows in Go.

### 5.3 Explicit policy functions

The initial policy API remains small:

- `CanViewArea(actor, areaPolicy) bool`
- `CanViewTopic(actor, topicState) bool`
- `CanCreateTopic(actor, areaPolicy) bool`
- `CanReply(actor, areaPolicy, topicState) bool`
- `CanEditPost(actor, postOwnership, postState) bool`
- `CanDeletePost(actor, postOwnership, postState) bool`
- `CanModerate(actor) bool`
- `CanAdminister(actor) bool`

Read queries still enforce access in SQL. These functions govern mutation and
provide unit-testable explanations. Do not create a stringly typed
`Can(actor, action, resource)` framework for seven operations.
For publishing predicates, `AccessContext.MutedUntil != nil` means the snapshot
builder has already established an active mute at `ValidatedAt`; expired mute
timestamps are normalized to nil before policy evaluation. `CanCreateTopic`
requires valid authenticated visibility, rejects suspension or active mute,
allows members only in normal areas, allows staff in normal/read-only areas,
and rejects archived areas for every actor until restoration changes policy.

## 6. Database specification

All timestamps use PostgreSQL `timestamptz` in UTC. Primary keys use generated
64-bit integers unless an external/public identifier requires a documented
alternative. User-facing routes shall not trust sequential IDs as permission.

### 6.1 `users`

Required columns:

- `id`
- `display_name`
- `email` nullable and non-authoritative
- `avatar_url` nullable
- `bio` with a bounded length
- `role`
- `suspended_at`, `suspended_until`, `suspension_reason` nullable
- `muted_until` nullable
- `created_at`, `updated_at`, `last_login_at`

Role has a check constraint. Display/profile fields have explicit length
limits. Suspensions do not delete the row.

### 6.2 `governance_state`

One seeded singleton row exists solely as a transaction lock for
administrator-continuity decisions. Its key is a boolean primary key constrained
to `true`, so the schema permits at most one row; the initial migration inserts
that row. PostgreSQL locking clauses require UPDATE privilege on at least one
selected column. The runtime role therefore receives only column-level
`UPDATE(singleton)` through `deploy/postgresql/runtime-grants.sql`; it has no
table-wide UPDATE, no UPDATE on `created_at`, and no DELETE privilege. The key
constraint admits only `true`, so this privilege cannot encode mutable state.
Readiness fails if exact cardinality one is not observed or no active
administrator exists. Bootstrap and every role or suspension transition that
can change the active-administrator set lock this row with `SELECT ... FOR
UPDATE` before evaluating state. An active
administrator has `role = administrator` and no suspension effective at the
transaction time. The row contains no cached administrator count that could
drift.

### 6.3 `external_identities`

- `user_id` foreign key and unique for version 1.0 single-issuer operation
- `issuer`
- `subject`
- `created_at`, `last_verified_at`
- unique `(issuer, subject)`

Issuer and subject are not user-editable.

### 6.4 `forum_groups` and `forum_group_members`

`forum_groups` contains a stable generated ID, unique bounded name, creator,
and timestamps. `forum_group_members` contains `group_id`, `user_id`, the
administrator that granted membership, and timestamps, with primary key
`(group_id, user_id)` and an index on `(user_id, group_id)`. Role and membership
changes append audit events in the same transaction.

### 6.5 `sessions`

- `id`
- `token_hash` unique
- `user_id`
- `issued_at`, `last_seen_at`, `validated_at`, `expires_at`
- `revoked_at` nullable
- bounded `user_agent_hash` and `ip_prefix` or equivalent audit fields

Only a cryptographic hash of the opaque token is stored. Session lookup uses a
constant-time comparison where application comparison is required. Expired and
revoked sessions never authenticate.

### 6.6 `oidc_login_attempts`

- hashed/state lookup key
- protected nonce and PKCE verifier material
- purpose (`login` or `revalidate`) and existing session reference when needed
- `return_path`, structurally constrained by PostgreSQL to an internal browser
  path and validated by the application against the exact configured base path
  and canonical encoding before storage
- `created_at`, `expires_at`, `consumed_at`

State is single use. Cleanup is bounded and safe to repeat.

### 6.7 `areas` and `area_groups`

`areas` includes:

- `id`, unique `slug`, `name`, `description`
- `display_order`
- `visibility`, `posting_mode`
- `created_by`, `updated_by`, timestamps

`area_groups` uses primary key `(area_id, group_id)` and foreign keys to the
local group relation.

Constraints:

- Group mappings may exist only for a group-visible area, or are ignored only
  under an explicit migration rule. Prefer rejecting inconsistent state.
- Slugs are normalized, bounded, and immutable after publication unless a
  redirect record is added. Version 1.0 may therefore prohibit slug changes.

### 6.8 `topics`

- `id`, `area_id`, `author_id`, `title`, normalized optional slug
- `state` such as `open`, `locked`, `hidden`, `archived`
- `pinned_at` nullable
- `first_post_id`, `latest_post_id` established within creation transaction
- `reply_count`, `next_post_number`
- `created_at`, `updated_at`, `last_activity_at`, `deleted_at` nullable

Indexes support area lists ordered by pinned/activity and recent activity
ordered globally after access filtering.

Topic creation renders and validates the first post before opening a
transaction, then locks the current area row and its group mappings before
calling `CanCreateTopic`. One data-modifying statement allocates the topic and
post identities and inserts the mutually referencing open topic and post 1;
the existing deferred constraints validate the complete pair at commit. The
topic begins with reply count 0 and next post number 2. Ordinary publication is
not a moderation transition and does not create a moderation-audit row.

### 6.9 `posts`

- `id`, `topic_id`, `author_id`, `post_number`
- `markdown_source`
- `rendered_html`, `renderer_version`
- `revision`
- `created_at`, `updated_at`, `edited_at`, `deleted_at`
- `deleted_by`, `deletion_reason` nullable
- unique `(topic_id, post_number)`

Source and rendered sizes have limits. A post edit increments `revision` and
uses `WHERE revision = $expected` to detect lost updates.

Author edit is deliberately not a staff content-rewrite power. The service
validates and renders the bounded draft before opening one transaction, then
locks the undeleted post, topic, owning area, and current area-group policy.
It requires the actor to remain able to view the area and to own the visible
post; suspended or actively muted authors cannot edit. Only after that
authorization does it compare the submitted positive revision with the locked
current revision, so a conflict response cannot reveal an unauthorized post.
One guarded update replaces source/rendered values, increments revision, and
sets nondecreasing `updated_at`/`edited_at`. Missing, deleted, hidden from the
actor, and foreign-owned posts share the generic denial/not-found boundary.
Edits do not change topic reply/activity counters and do not let preview state
cross the transaction boundary.

The browser exposes Edit only for an active authenticated post owner. The edit
loader returns current source/revision only through the same area/group and
hidden-topic visibility predicates as reads; it is presentation authority and
the apply transaction always reauthorizes. Edit GET, preview, and apply require
a fresh local session and no-store responses. Preview verifies CSRF before any
target lookup, preserves the submitted revision, sanitizes through the exact
post renderer, and mutates nothing. Apply returns `409 Conflict` for an
authorized stale revision while preserving the escaped draft; HTMX explicitly
swaps that conflict form. Successful ordinary/HTMX submissions navigate to the
exact canonical topic page and post fragment.

Author deletion is a distinct owner-only soft-delete transition; staff removal
belongs to audited moderation and does not borrow author authority. The browser
shows the POST control only to an active owner with a session CSRF token. The
request carries the displayed revision, verifies CSRF before parsing, and the
service then locks and reauthorizes the same current post/topic/area/group state
before disclosing a revision conflict. The guarded update retains identifiers,
source, rendered content, revision, and topic counters while setting
`deleted_at`, `deleted_by`, and the fixed reason `Deleted by author`.
`deleted_at` cannot precede create/update/edit time. Success navigates to the
topic root because the deleted post no longer has a visible fragment.

Reply creation renders and validates before opening a transaction, then locks
the undeleted topic row for update, the owning area row for share, and the
area's current group mappings for share. `CanReply` evaluates that locked
policy. The held topic lock serializes allocation from `next_post_number`; one
statement inserts the reply and advances `latest_post_id`, `reply_count`,
`next_post_number`, and activity timestamps. The persisted reply/activity time
is the greater of the caller's bounded UTC timestamp and existing topic
activity, so a request that waited on the lock cannot move chronological post
timestamps or area-list activity backward. There is no automatic retry after
an unknown commit outcome.

### 6.10 `topic_reads`

- `user_id`, `topic_id`, `last_read_post_number`, `read_at`
- primary key `(user_id, topic_id)`

Updates use `GREATEST` so an out-of-order request does not mark a topic less
read.

### 6.11 Reports and audit

`reports` identifies exactly one supported target type and target ID, records a
bounded reason, workflow status, assignment, and resolution.

`moderation_actions` includes:

- closed `actor_kind` (`forum_user` or `operator`) with a check constraint that
  requires exactly one matching actor identifier
- target identifiers
- closed action type
- required bounded reason where applicable
- previous and resulting state as bounded structured data
- request ID and timestamp

Application roles have no UPDATE or DELETE path for audit rows. Database-level
privilege separation is evaluated before stable 1.0.

## 7. Access-controlled SQL

Every repository returning area-owned data accepts `AccessContext`. The access
predicate is shared as reviewed SQL generation or repeated explicitly; it is
not applied in Go after fetching rows.

Representative shape:

```sql
WHERE
    sqlc.arg(is_staff)::boolean
    OR a.visibility = 'public'
    OR (
        sqlc.arg(is_member)::boolean
        AND a.visibility = 'authenticated'
    )
    OR (
        sqlc.arg(is_member)::boolean
        AND a.visibility = 'groups'
        AND EXISTS (
            SELECT 1
            FROM area_groups ag
            WHERE ag.area_id = a.id
              AND ag.group_id = ANY(sqlc.arg(group_ids)::bigint[])
        )
    )
```

Requirements:

- Empty group arrays are handled explicitly.
- Count and search queries contain the predicate before aggregation/ranking.
- Staff bypass is an explicit boolean derived from a verified role, not a
  caller-supplied request parameter.
- Query tests compare results for every access-matrix role.
- Not-found and unauthorized direct reads have indistinguishable public
  behavior.

`ListVisibleAreas` is the first concrete repository primitive. It applies this
predicate before ordering by `(display_order, id)`, treats nil and empty group
arrays as no group authority, and returns full area rows only after PostgreSQL
has removed restricted rows. Its booleans and group IDs are internal repository
facts; browser parameters never bind them directly. The exported store boundary
first requires `AccessContext.Valid`, then derives `is_member` from canonical
authentication and `is_staff` only from the closed moderator/administrator
roles. Query failures discard any partial row slice.

## 8. OIDC implementation

### 8.1 Discovery and validation

At startup, load discovery only from the supported per-provider
`OIDC_ISSUER_URL`. Validate that the returned issuer matches exactly. Do not
accept request-provided issuers. Cache keys
according to the chosen library's documented behavior and expose discovery
failure through readiness only when it prevents safe operation.

Discovery, token, and JWKS requests use one ten-second HTTP client, refuse
redirects, reject endpoints outside the configured issuer origin, and cap each
response body at 512 KiB. Discovery must advertise at least one signing algorithm
supported by `go-oidc`. Confidential clients pin `client_secret_basic`; local
public-client development pins the explicitly advertised `none` method rather
than relying on OAuth2 authentication-style probing.

### 8.2 Login attempt

- Generate at least 256 bits of random state and nonce material.
- Generate a PKCE verifier using a cryptographic random source and S256
  challenge.
- Expire attempts after a fixed five minutes and limit outstanding attempts per
  browser/session.
- Validate return paths against the configured base path; never redirect to an
  arbitrary absolute URL.
- Record whether the attempt is initial login or session revalidation.
- Initial `GET /login` accepts either no query or exactly one canonical
  `return` value inside the application subtree. No query defaults to the
  application root. The raw query is capped at 8,192 bytes and is rejected
  before entropy or database work when malformed, duplicated, or unsafe.
- Initial login state is bound to one host-only, `HttpOnly`, `SameSite=Lax`
  cookie derived from the configured session-cookie name, scoped to the
  application cookie path, and limited to five minutes. Production sets
  `Secure`; loopback HTTP development does not. A new start overwrites that
  browser's prior usable state.
- Revalidation attempt creation receives the positive session ID only from the
  authenticated server request snapshot, rewrites the protected attempt
  metadata to `purpose=revalidate`, and stores that ID as the required foreign
  key. The authentication service exposes this as one redacted, cancellation-
  preserving start operation and builds the same state/nonce/PKCE authorization
  URL as initial login. Browser input can select only a validated internal
  return path. Its five-minute state cookie uses the fixed
  `_oidc_revalidate_state` suffix, distinct from initial login's `_oidc_state`;
  only server route construction selects either namespace.

### 8.3 Callback transaction

The shared callback admits exactly one `state` and `code` query value and
requires exactly one matching unquoted state cookie across the fixed initial-
login and revalidation namespaces. Duplicate cookies within either namespace,
two matching namespaces, or no matching namespace fail before database or
provider work. The selected service operation still requires the same purpose
from the consumed PostgreSQL row, so the cookie selects an expected path but
cannot rewrite durable purpose. Revalidation additionally requires exactly one
unquoted canonical old session cookie before completion. Once state and its
encoding are valid, the handler emits expiration of only the selected transient
cookie before invoking completion; success and every later failure therefore
rotate that browser state away.

The callback:

1. Atomically consumes the login attempt.
2. Exchanges the code once.
3. Validates the ID token and nonce.
4. Validates claim types and configured bounds.
5. Requires a non-empty subject and issuer.
6. Validates approved profile claim types and bounds.
7. Creates a newly verified identity with `RoleMember`, or updates an existing
   identity/profile without modifying its forum-local role/group membership,
   and creates the session in one transaction.
8. Rotates cookies and redirects only to the validated internal return path.

Failure after code exchange creates no authenticated browser state.

The identity/session transaction first acquires a transaction-scoped PostgreSQL
advisory lock on the separately hashed verified issuer and subject. A hash
collision may serialize unrelated logins but cannot merge identities. Under the
lock it selects or creates the external identity, updates only approved profile
and verification timestamps for an existing user, preserves local role/group/
suspension state, and inserts the new session before one commit. Unknown commit
outcomes are never retried automatically.

The accepted identity snapshot contains only verified `iss`, `sub`, `name`,
`email` when `email_verified=true`, and `picture`. Bounds match the database:
issuer 2,048 characters, subject 512, display name 80, email 320, and avatar URL
2,048. Profile claim types are strict; controls are rejected. The avatar must be
a canonical absolute HTTP(S) URL without credentials or a fragment, and an
HTTPS issuer cannot supply an HTTP avatar. OIDC role,
group, permission, entitlement, and similar claims are not decoded into the
accepted type and cannot mutate forum-local authorization.

Consumption is one conditional PostgreSQL `UPDATE ... RETURNING`: the state
hash must exist, remain unconsumed, have `created_at <= now`, and have the
exclusive expiry `expires_at > now`. Missing, future, expired, and replayed
attempts all return the same no-row result. One concurrent caller can win. The
consumer requires an exact expected purpose before the query. After consuming,
initial login rejects any session binding, while revalidation requires one
positive stored session ID; either mismatch burns the attempt and returns no
recovered nonce or verifier.

For revalidation, the callback additionally verifies the existing session,
updates the identity/profile snapshot, creates a rotated replacement session,
and revokes the old session in the transaction. A failed or abandoned
reauthorization leaves the stale session unable to authorize protected routes.
The completion boundary runs exactly one attempt consumption, one authorization-
code exchange, and one rotation in that order; it retries none of them and
returns no replacement browser state unless every stage succeeds.
The authentication service owns every dependency for that sequence and exposes
only the replacement opaque token, previously validated internal return path,
and fresh absolute expiry to the HTTP boundary.
The rotation transaction first selects the exact session ID plus SHA-256 hash
of the old browser token, requires current absolute/idle lifetime and current
local nonsuspension, rejects issue/activity/validation timestamps in the future,
joins the sole stored issuer/subject, and row-locks the
session, user, and identity together. Missing, revoked, expired, idle, suspended,
or mismatched state is the same no-row failure before profile or session writes.
After the replacement insert succeeds in that transaction, revocation targets
the same positive session ID and old-token hash, requires it still unrevoked and
unexpired at the transaction timestamp, and must affect exactly one row before
commit. The replacement is issued and validated at that single transaction
timestamp and receives a fresh absolute expiry of that timestamp plus the
configured session maximum age; it does not inherit the old session's remaining
lifetime after successful fresh OIDC verification.

The first administrator can be granted through the exact-subject browser setup
or the explicit operator fallback. Both operations lock the same governance
singleton and require both zero historical `bootstrap_administrator` audit rows
and zero active administrators. This makes the bootstrap permanently one-time,
including after later account suspension or database repair. Concurrent browser
and operator attempts serialize; exactly one may commit.

`GET /setup` and `POST /setup/administrator` are no-store routes. When setup is
open, anonymous GET redirects through `/login?return=/setup`; an authenticated
but stale session redirects through revalidation. Only a current local member
whose stored issuer and subject match `OIDC_ISSUER_URL` and
`BOOTSTRAP_ADMIN_SUBJECT` receives the confirmation form. POST requires the
session-derived CSRF value, a fresh session, and a generated request ID. The
transaction rechecks the exact positive session ID, user ID, issuer, subject,
active account state, bootstrap history, and administrator count under locks;
then changes the role, writes one `actor_kind=forum_user` audit with actor and
target equal to that user, and revokes the elevating session before commit.
After success the handler expires the cookie and redirects through a fresh OIDC
login. Failure commits none of those writes and exposes no configured identity.

The operator fallback targets an already provisioned `(issuer, subject)`, emits
`actor_kind=operator`, obeys the same history/administrator closure checks, and
never invents a forum-user actor. Both exported operations return IDs only after
commit and never retry an unknown outcome. Normal administrator-role and
suspension transitions lock the same governance row and reject a result with
zero active administrators. OIDC claims do not bootstrap or restore local
privileges.

### 8.4 Authentik self-registration

`GET /register` performs no local account mutation. It redirects to the exact
configured Authentik enrollment flow with the absolute local `/login` endpoint
as the sole `next` value. The Authentik blueprint uses prompt, user-write, email
verification, and user-login stages; the user-write stage creates an inactive
external user directly in `gotth-bb-users`, and email verification activates
the account. The board application has an `any`-mode binding to that group.

Enrollment activation is an operational gate: inventory every sibling
Authentik application and require at least one explicit enabled access binding
that excludes `gotth-bb-users`. An unbound sibling application blocks claiming
site-only registration because Authentik grants it to all users by default.
The enrollment blueprint is applied only after the designated first
administrator has completed setup. The OIDC callback remains the sole local
account creation path and always creates `RoleMember`.

## 9. Session implementation

- Cookie value contains only a 256-bit random opaque token encoded as 43
  unpadded base64url characters. PostgreSQL stores only SHA-256 of those exact
  encoded cookie bytes.
- Each authenticated request derives its CSRF synchronizer token as
  HMAC-SHA-256 keyed by the decoded 256-bit session secret over the fixed ASCII
  domain `gotth-bb/csrf/v1`, then exposes only the 43-character unpadded
  base64url digest to forms. The derived value cannot authenticate a session,
  requires no second durable secret, and rotates whenever the session rotates.
- The session-loading boundary places the derived value in private request
  context only after the opaque credential authenticates. Anonymous requests
  receive no CSRF authority. An internally inconsistent authenticated result
  paired with a malformed credential fails 500 and expires that browser state.
- The same authenticated snapshot carries the positive local session ID already
  returned by the indexed lookup. It is server-internal correlation authority
  for revalidation-attempt binding; it is never rendered or accepted from a
  browser.
- Unsafe browser requests validate either exactly one `X-CSRF-Token` header
  (HTMX) without touching the body, or exactly one `_csrf` field in an
  `application/x-www-form-urlencoded` ordinary-form body after a route-specific
  bound is applied. The form body is restored byte-for-byte for later parsing.
  Missing, duplicate, malformed, or mismatched values fail before mutation;
  comparison is fixed-length and constant-time.
- Production cookie flags: `Secure`, `HttpOnly`, `SameSite=Lax`, with `Path`
  equal to the configured base path or `/` for an empty base path.
- Session rotation occurs after login and any future privilege elevation.
- Once `AUTH_REVALIDATE_INTERVAL` elapses, protected routes require a fresh OIDC
  authorization before the session can authorize them. Public reads may proceed
  anonymously while revalidation is incomplete.
- Logout revokes server state before expiring the browser cookie.
- A suspended user fails session authentication immediately after local state
  is observed.
- Cleanup deletes expired/revoked sessions in bounded batches and is safe to
  run repeatedly.
- Session reads update `last_seen_at` at a throttled interval to avoid one write
  per request. Alpha.1 uses a five-minute maximum threshold, reduced to half
  the configured idle timeout when that is shorter: equality is due, the normal
  hot path performs only the indexed read, and the conditional update rechecks
  revocation and absolute expiry.

## 10. HTTP and route contract

Internal routes are shown relative to the configured external base URL.

| Method | Route | Purpose | Minimum actor |
| --- | --- | --- | --- |
| `GET` | `/` | Area index | Visitor |
| `GET` | `/login` | Start Authentik login | Visitor |
| `GET` | `/register` | Redirect to the fixed Authentik enrollment flow | Visitor |
| `GET` | `/auth/callback` | OIDC callback | Login attempt |
| `GET` | `/auth/revalidate` | Start session-bound Authentik revalidation | Member session |
| `GET` | `/setup` | First-administrator confirmation | Designated fresh member session |
| `POST` | `/setup/administrator` | Claim first administration and revoke elevating session | Designated fresh member session |
| `POST` | `/logout` | Revoke local session | Member |
| `GET` | `/areas/{slug}` | Area topic list | Area viewer |
| `GET` | `/topics/{id}` | Topic and posts | Area viewer |
| `GET` | `/topics/new` | New-topic form | Eligible member |
| `POST` | `/topics/preview` | Preview new-topic draft | Member session |
| `POST` | `/topics` | Create topic | Eligible member |
| `POST` | `/topics/{id}/replies/preview` | Preview reply draft | Member session |
| `POST` | `/topics/{id}/replies` | Create reply | Eligible member |
| `GET` | `/posts/{id}/edit` | Edit form | Author |
| `POST` | `/posts/{id}/edit/preview` | Preview edit draft | Author |
| `POST` | `/posts/{id}/edit` | Apply edit | Author |
| `POST` | `/posts/{id}/delete` | Author soft delete | Author |
| `POST` | `/reports` | Create report | Member |
| `GET` | `/moderation/reports` | Moderation queue | Moderator |
| `POST` | `/moderation/actions` | Moderation transition | Moderator |
| `GET` | `/moderation/users/{id}` | Local account status | Moderator |
| `POST` | `/moderation/users/{id}/suspend` | Suspend local account | Moderator |
| `POST` | `/moderation/users/{id}/reinstate` | Reinstate local account | Moderator |
| `GET` | `/admin/areas` | Area management | Administrator |
| `POST` | `/admin/areas` | Create area | Administrator |
| `POST` | `/admin/areas/{id}` | Change area | Administrator |
| `GET` | `/search` | Access-filtered search | Visitor |
| `GET` | `/health/live` | Liveness | Edge/operator |
| `GET` | `/health/ready` | Readiness | Edge/operator |

`POST /logout` validates CSRF before reading or revoking the session token. A
verification failure performs no revocation, expires no cookie, and returns an
empty `303` to the application root with the fixed
`logout=verification-failed` query. The root renders that marker only for an
authenticated session as an explicit notice that logout failed and the user
remains logged in. The marker carries no authority and is never interpreted as
evidence of a completed logout.

The new-topic form requires exactly one canonical `area=<slug>` query on
`GET /topics/new`; the slug is preserved as a hidden field but is never treated
as authority. Topic and reply forms are URL-encoded and capped at 262,144 wire
bytes so percent-encoding can carry the 65,536-byte decoded Markdown maximum.
CSRF verification consumes and restores the bounded bytes before any form
parsing or publisher call. Parsing then rejects missing, duplicated, or unknown
fields. Only the server-owned session snapshot reaches the publishing service.

Publishing validation returns `422` with the exact submitted title/Markdown
escaped back into the full page or HTMX fragment and a field-specific message.
Authorization, missing-target, malformed-form, CSRF, and storage failures do
not echo submitted source. Successful ordinary forms use `303` to the
builder-owned topic/post URL. Successful HTMX mutations use `204` plus a
same-origin `HX-Location` object containing the canonical path,
`target: "#main-content"`, and `swap: "outerHTML"`. HTMX then performs one
authoritative `GET`, swaps only the main region, and pushes the canonical URL
without reloading the document. Login, revalidation, and other session-boundary
transitions deliberately retain `HX-Redirect`. Eligible area/topic pages
expose the actions; the locked transaction policy remains authoritative if
state changes after rendering.

Both publishing forms offer a progressive-enhancement preview action. Preview
uses the same bounded draft validation and sanitized server renderer as final
publication, performs no PostgreSQL operation, and returns the original escaped
source plus only opaque trusted sanitized HTML. Ordinary preview returns a full
`200` form page; HTMX preview returns the equivalent `200` form fragment.
Invalid drafts return the same `422` field errors and escaped source as final
publication. Preview routes share the exact authentication, revalidation,
canonical-path, CSRF-first, form-bound, and strict-field rules of their final
publication routes; they never create durable preview state or weaken the
transaction's final authorization decision.
Preview is only a local transform of submitted text: it does not probe target
existence or grant target access. Action links are shown from eligible pages,
while a direct member preview remains harmless and final publication still
decides current target existence and authority under lock.

Area topic lists use conventional one-based `page` query parameters. An absent
parameter means page 1; the only accepted spelling is an unsigned base-10
integer without signs or leading zeros, in the closed range 1 through 10,000.
Each page contains at most 25 topics. Malformed, overflowed, zero, excessive,
or empty later pages use the same `404` response as a missing or inaccessible
area. PostgreSQL receives only the resulting bounded offset and fixed limit.

Topic pages use a positive canonical base-10 `int64` topic ID path segment.
Signs, leading zeros, overflow, escaped paths, extra segments, and zero are not
normalized and share the same `404` response as a missing, deleted, hidden, or
area-inaccessible topic. The page query uses the same canonical spelling and
1-through-10,000 bound as area topic lists. Each page contains at most 25
nondeleted posts in ascending immutable `post_number` order; an empty first page
is permitted for a visible topic whose posts are all soft-deleted, while an
empty later page is `404`. PostgreSQL receives a maximum offset of 249,975 and
the fixed limit only.

Topic metadata and the post page come from one access-filtered statement and
one PostgreSQL snapshot. It joins the owning area, applies the complete
server-derived member, staff, and group authority before returning topic text,
and excludes deleted topics, hidden topics for nonstaff, and deleted posts. A
left-joined null post row represents only a visible topic with zero visible
posts on page 1; offset pages receive no sentinel and therefore return `404`.
The window count and every breadcrumb field come from that same authorized
result, so a concurrent policy commit cannot produce mixed-authority metadata
and posts. Topic canonical URLs use `/topics/<topic-id>` and omit `page=1`.
Stable post URLs append `#post-<post-id>` to that topic page, including the
canonical page query when required. Breadcrumb area links use the immutable
area slug returned by the same access-controlled result.

Use POST for browser mutations. Method override tricks are not required in
version 1.0. Route bodies, path IDs, query lengths, and pagination sizes are
bounded.

The browser router activates `/login`, `/register`, `/auth/callback`,
`/auth/revalidate`, `/setup`, `/setup/administrator`, and `/logout` as exact
paths. Session lookup wraps only exact routes that can
consume identity: `/`; one-segment `GET /areas/{slug}`; canonical
positive-decimal one-segment `GET /topics/{id}`; the exact publishing,
preview, edit, delete, topic-moderation, account-status, suspend, and reinstate
routes listed above; setup; revalidation; and logout. Every numeric identifier must
pass the canonical parser before session lookup. Noncanonical escaped paths,
malformed or nested paths, wrong methods, health, static, and unknown paths go
directly to the public router and cannot become unavailable merely because the
session store is unavailable. No broader `/areas/`, `/topics/`, `/posts/`, or
`/moderation/` prefix receives session authority.

## 11. Full-page and HTMX responses

- Every browser-facing service response, including request-ID failure, panic
  recovery, `404`, `405`, validation, HTMX, static, and health responses,
  receives the same fixed browser boundary before handler execution:
  `default-src 'none'`, `base-uri 'none'`, `form-action 'self'`,
  `frame-ancestors 'none'`, `object-src 'none'`, and self-only script, style,
  image, font, connection, and manifest sources (with `data:` additionally
  allowed for images). Responses also send `nosniff`, `DENY` framing,
  `no-referrer`, same-origin opener/resource isolation, origin-agent isolation,
  and a deny-by-default camera/geolocation/microphone/payment/USB permissions
  policy. HSTS remains Caddy-owned because the application transport is
  deliberately loopback HTTP.
- Middleware order is outer-to-inner: browser security headers, generated
  request ID, access logging, panic recovery, route-pattern bridge, Chi routing,
  then the matched handler. This lets recovery clear unsafe application headers
  while restoring the preinstalled browser policy on its bounded `500`.
- `HX-Request` selects a documented fragment only after the same handler,
  authorization, validation, and service path runs.
- Fragment selection requires the exact `HX-Request: true` value. An exact
  `HX-History-Restore-Request: true` always selects a full document, and both
  headers are added to `Vary` whenever they can affect the representation.
  The page config disables `historyRestoreAsHxRequest` so history cache misses
  request full documents under HTMX's documented contract.
- The HTMX page configuration is valid JSON and disables eval, response script
  processing, injected indicator styles, and client-side history DOM storage;
  requests remain same-origin. It enables native form-validity reporting and
  explicitly swaps `422` validation fragments while retaining the non-success
  status/error classification. HTML responses are
  `private, no-store`; release-versioned CSS and JavaScript are immutable for
  one year.
- Full-page successful form submission uses a `303` redirect.
- Successful in-session HTMX mutations return a fragment directly or a
  same-origin `HX-Location` navigation targeting `#main-content`; browser-level
  `HX-Redirect` is reserved for authentication, revalidation, and other
  session-boundary transitions.
- Validation returns `422` with the form and field errors for both modes.
- Authentication required returns a safe login redirect for full pages and an
  equivalent explicit HTMX redirect.
- Authorization/not-found behavior remains identical between modes.
- Errors never return a successful fragment containing an error message.

## 12. URL builder

One URL builder owns:

- Normalization and prefixing with `BASE_PATH`.
- Absolute URL construction from `PUBLIC_BASE_URL`.
- Escaping path segments and query values.
- Canonical topic/post URLs.
- OIDC callback and post-logout return URLs.

The builder accepts route components, not arbitrary untrusted URLs. Tests scan
rendered core pages for root-relative application links that omit `/bb`.

## 13. Markdown rendering

- Canonical input is nonblank UTF-8 Markdown source from 1 through 65,536
  bytes. Sanitized rendered HTML must be nonblank and no larger than 262,144
  bytes before persistence.
- Alpha.1 uses Goldmark v1.8.5 in plain CommonMark mode. Raw HTML and dangerous
  links retain Goldmark's default disabled behavior; no GFM tables, task lists,
  automatic heading IDs, linkifier, or runtime extension is enabled.
- Link schemes are restricted to an allowlist.
- Rendered HTML passes through a narrow sanitizer allowlist even when the parser
  claims safe output.
- Every surviving link receives `nofollow noreferrer`, avoiding a second
  browser-versus-Go external-URL classification policy.
- Rendered output carries the exact renderer version
  `goldmark-v1.8.5-bluemonday-v1.0.27-p1` for deterministic rebuilding.
- Templates receive rendered content through one explicit trusted-HTML type;
  arbitrary strings cannot opt out of escaping.

## 14. Moderation transitions

Moderation actions are closed typed transitions, not arbitrary field patches.
Each service validates actor, target state, allowed transition, and required
reason. It then mutates and appends the audit event in the same transaction.

Examples:

- `HidePost`, `RestorePost`, `RedactPost`
- `LockTopic`, `UnlockTopic`, `PinTopic`, `UnpinTopic`, `MoveTopic`
- `WarnUser`, `MuteUser`, `SuspendUser`, `ReinstateUser`
- `ChangeAreaVisibility`, `ChangeAreaPostingMode`, `ChangeAreaGroups`

Idempotent repetition returns the current state without duplicating the audit
event only when the action contract explicitly defines it as idempotent.

Alpha topic lock/unlock is a strict transition rather than an idempotent setter:
`open -> locked` and `locked -> open` are the only admitted pairs. An active
moderator or administrator supplies a nonblank, single-line UTF-8 reason of at
most 2,000 bytes without leading or trailing whitespace, and the
server-generated request UUID. One transaction locks the undeleted topic,
rejects every other current state as a conflict, then changes state and appends
the typed `lock_topic` or `unlock_topic` audit row in one data-modifying
statement. The effective update/audit time is nondecreasing across lock waits.
Any update, audit, scan, or commit failure rolls back both records. Browser
controls appear only on an open/locked topic page for active moderator or
administrator presentation authority. The exact POST-only lock/unlock routes
require a current local session, Authentik revalidation, the session CSRF
token, a bounded strict form containing only the canonical reason, and the
server request identifier decoded as the audit UUID. The service transaction
remains final authority. Success validates the committed result and navigates
to the canonical topic URL; conflicts require a reload and never retry.

Alpha topic hide/restore is likewise strict: only `open -> hidden` and
`hidden -> open` are admitted. A locked topic must first be explicitly unlocked
before hiding, so restoration never guesses at or silently discards prior lock
state. Both directions require the same canonical reason and request UUID and
use the same row-lock, guarded update, atomic audit, monotonic-time, and
no-retry mechanism as lock/unlock. Browser controls use exact POST-only
`hide`/`restore` routes behind the same current-session, Authentik
revalidation, CSRF, bounded-form, request-UUID, typed-error, and validated
redirect boundary. Active staff see Lock and Hide on open topics, Unlock on
locked topics, Restore on hidden topics, and no transition control on archived
topics; the transaction remains final authority.

Alpha local-account suspension/reinstatement is a strict audited transition.
Suspension means an indefinite local suspension (`suspended_until` is null);
reinstatement explicitly clears all three suspension fields. Both directions
require a canonical nonblank single-line UTF-8 reason of at most 500 characters
and 2,000 bytes plus a nonzero server request UUID. Repetition of the effective
current state is a conflict and creates no audit.

An active moderator may act only on a member. An active administrator may act
on any other local account but never itself, and suspension may not leave zero
active administrators. The transaction locks the governance singleton, then
the actor and target user rows in ascending user-ID order. It revalidates the
actor's persisted role, suspension, and mute state rather than trusting the
request's session snapshot. One guarded data-modifying statement changes the
target and appends the typed `suspend_user` or `reinstate_user` audit with exact
previous/resulting suspension JSON. Its effective update/audit timestamp is
nondecreasing across target-row lock waits. Authorization and effective
suspension state are evaluated against the request observation time, never the
later persistence timestamp, so a future target update cannot expire an
actor's mute or activate a scheduled state early. Any lock, count, update,
audit, scan, or commit failure rolls back the entire operation, and an unknown
commit outcome is never retried. A committed suspension immediately removes
every existing session for that user from active-session lookup without
deleting the sessions or authored content; reinstatement makes otherwise-valid
sessions eligible again.

The alpha account-status read model returns one target by positive local user
ID without exposing email or identity-provider attributes. Active moderators
may read only another member; active administrators may read any other local
account. Self, unauthorized, malformed, and missing targets share one no-row
result. SQL applies the role/target filter before returning the fixed-width
row, and the store boundary independently closes role, timestamps, suspension
consistency, and effective observation-time status before presentation.

The exact account-status browser route requires a current local session and
Authentik revalidation and rechecks the typed read model's structural
presentation invariants before rendering. Active staff receive non-self
account-status links beside post authors; the privacy-bounded query remains
final authority, so a moderator
cannot use a link or direct route to inspect another staff account. The page
contains exactly one bounded reason form for the target's current effective
state. Exact POST-only suspend/reinstate routes verify session CSRF before
strict form parsing, use the server request identifier as the audit UUID, map
typed service failures without target disclosure, validate the committed
result, and navigate to the builder-owned account-status URL. The transaction
remains final authority if target or actor state changes after rendering.

## 15. Migrations

- Migration filenames are ordered, contiguous, and immutable after a database
  consumes them. Startup and the migration command reject missing versions,
  renamed files, changed applied bytes, unknown applied versions, and files
  larger than one MiB.
- Each migration documents lock and data-rewrite risk.
- Migration files contain no explicit transaction-control statements such as
  `BEGIN`, `COMMIT`, or `ROLLBACK`; the project-owned runner owns that boundary.
  Migration SQL is trusted reviewed release code, not a sandboxed input.
- Transactional DDL is used when PostgreSQL permits it.
- Destructive schema removal uses expand/migrate/contract across compatible
  releases, not a single irreversible deploy.
- A fresh database and an upgrade from the previous release are tested.
- Down migrations are supplied only when they are safe and honest. Data-loss
  rollback uses restore or forward repair and says so explicitly.

## 16. Logging and errors

Structured request logs contain timestamp, severity, request ID, route name,
method, status, duration, authenticated user ID when permitted, and error class.
They do not contain cookies, authorization codes, tokens, client secrets,
Markdown bodies, or unrestricted query strings.

Request IDs contain 128 random bits encoded as 32 lowercase hexadecimal bytes.
The edge ignores inbound `X-Request-ID`; entropy failure returns 503 before the
application runs. Completion logs record only the matched route pattern,
method, status, response-byte count, whole-millisecond duration, and request
ID. A downstream `http.ErrAbortHandler` is propagated unchanged and produces
one bounded `request aborted` event with error class `abort`, route, method,
byte count, duration, and request ID but no fabricated HTTP status. These logs
never include the raw request target or query.

Panic logs contain a fixed error class, request ID, and stack, but never format
the recovered value. An uncommitted panic returns a bounded 500 containing its
request ID after discarding application-added response headers and restoring
only the pre-application header snapshot. After response commitment, recovery
re-panics with `http.ErrAbortHandler` so `net/http` quietly closes the
connection instead of appending a corrupt second response, leaking the original
value, or writing a duplicate server stack log. Middleware response observation
is for ordinary HTTP responses; streaming and connection hijacking are outside
1.0.0-alpha.1.

Public errors expose a request ID and useful next action without internal SQL,
filesystem, issuer, or stack details. Internal errors retain wrapped causes for
operator logs.

## 17. Health and shutdown

- Liveness reports whether the process event loop can serve requests.
- Readiness checks required configuration, migration compatibility, and a
  bounded PostgreSQL round trip.
- Pool startup uses the pgx-parsed immutable configuration, permits at most
  five seconds for its initial round trip, closes the new pool on failure, and
  transfers close ownership only after that round trip succeeds.
- After pool ownership transfers, the executable performs bounded OIDC
  discovery through the narrow configuration constructor and builds the
  authenticated browser router before binding the listener. Authentication
  construction failure is redacted, preserves cancellation, closes the pool
  exactly once, and leaves no listening socket.
- The executable opens the pool before binding the HTTP listener, owns exactly
  one close on every later startup, serve, cancellation, or shutdown path, and
  redacts a pool-factory failure before it reaches the process diagnostic.
- Authentik availability does not necessarily make existing-session reads
  unready; login failures are reported separately.
- The internal HTTP server permits at most 5 seconds for request headers, 30
  seconds for complete request reads, 30 seconds for response writes, and 60
  seconds for an idle keep-alive connection. Parsed request headers are capped
  at 1 MiB. The default `OPTIONS *` shortcut is disabled so the application
  router owns method behavior.
- Shutdown stops accepting new requests, drains bounded in-flight work, closes
  the database pool, and exits nonzero when shutdown fails. HTTP drain has a
  15-second deadline followed by forced connection closure; either shutdown or
  forced-close failure is returned to the process boundary. Startup
  cancellation observed before the listener-to-server ownership transfer
  closes the listener and aborts startup. That ownership transfer is the
  startup linearization point; cancellation racing after it is handled as
  ordinary bounded shutdown and may race with the first `Serve` call. The first
  process termination signal is explicitly unregistered before it cancels the
  service context, so default handling is restored before graceful drain and a
  second signal terminates immediately.

### 17.1 Container runtime

- The release archive carries `deploy/container/Containerfile`,
  `deploy/container/entrypoint.sh`, and `deploy/container/compose.yml` from the
  exact release commit. The container build uses only those files and the
  release binaries already admitted into that archive; it does not rebuild
  source with an ambient toolchain.
- The application runtime base is Alpine 3.23.3 pinned by manifest digest. The
  image carries the forum, migration, and operator binaries, exact version and
  commit labels, and no secret or deployment-specific configuration.
- The entrypoint reads the database URL and OIDC client secret only from
  Compose secret files named by non-secret environment variables, exports them
  to the child process, and replaces itself with the selected release binary.
- The application service runs as numeric UID/GID 65532 in the host network
  namespace while retaining the production-enforced `127.0.0.1:18082`
  listener. It uses a read-only root filesystem and bounded temporary
  filesystem, drops every Linux capability, forbids privilege escalation, and
  reports container health through the public process-liveness endpoint. Host
  networking preserves the same host-local reachability as the prior native
  process; it is not presented as container network isolation.
- PostgreSQL remains a separate Compose service at the pinned PostgreSQL 17.10
  digest. Its existing `/tank/gotth-bb/postgres17` bind mount and loopback-only
  maintenance port remain unchanged during the transition, so application
  rollback does not require moving or rewriting database data.
- Caddy remains host-managed and continues to proxy the unchanged loopback
  address. Application logs use Docker's journald driver so structured output
  stays in the host journal. Docker daemon startup remains systemd-managed.
- A failed image build, Compose validation, migration check, service health
  check, or Caddy smoke test leaves the native systemd service available as
  rollback. The native unit is stopped only for the port handoff and is not
  deleted during alpha validation.

## 18. Test contract

### 18.1 Unit tests

- Policy matrix for all roles, visibility values, posting modes, suspension,
  lock, and ownership combinations.
- URL builder with empty/test/production prefixes and hostile segments.
- Configuration validation.
- Markdown sanitizer against XSS payloads and unsafe schemes.
- State transition and stale-revision behavior.

### 18.2 PostgreSQL integration tests

- Every access-controlled query against the full access matrix.
- Transaction rollback when audit insertion fails.
- Concurrent reply number allocation.
- Unique identity and login-attempt consumption.
- Topic-read monotonicity.
- Search/count leakage checks.
- Fresh and upgrade migrations.

### 18.3 HTTP tests

- OIDC start/callback failure and success with a controlled test issuer.
- CSRF rejection.
- Full-page and HTMX parity.
- Correct root and path-prefixed links, cookies, redirects, assets, and callback.
- Validation preservation and status codes.
- Not-found equivalence for missing and unauthorized resources.

### 18.4 End-to-end tests

- Authentik-backed login in a staging environment.
- Visitor/member/group/moderator/admin journeys.
- Deployment health and smoke test beneath Caddy.
- Backup and restore rehearsal before stable release.

## 19. Definition of implementation complete

A feature is not complete because its happy-path handler exists. It is complete
when:

- Requirement and design references are recorded.
- Permission and failure paths are implemented.
- Relevant tests cover changed behavior, edge cases, regressions, and failure
  paths, aiming for complete coverage of the touched surface.
- Migrations and rollback implications are recorded.
- Logs and diagnostics do not expose secrets or restricted content.
- Documentation and operator impact are updated.
- Review evidence identifies any explicit coverage gap and why it remains.
