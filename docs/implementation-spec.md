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
8. Every non-root post has one immutable same-topic parent and one immutable
   bounded tree path; display indentation is never treated as authority.

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
| `ACTIVITY_CURSOR_KEYRING_FILE` | Yes after AN-02 | Absolute path to the read-only cursor-keyring secret |
| `ABUSE_RULES_FILE` | Yes after AN-05 | Absolute path to the bounded read-only blocked-destination rules |
| `REQUEST_RATE_LIMIT` | Yes after AN-05 | Positive requests allowed per client window; initial value `300` |
| `REQUEST_RATE_WINDOW` | Yes after AN-05 | Process-local request window; initial value `60s` |
| `REQUEST_RATE_CLIENT_CAPACITY` | Yes after AN-05 | Maximum retained client windows; initial value `4096` |
| `PUBLISH_RATE_LIMIT` | Yes after AN-05 | Successful topics/replies per established-account window; initial value `10` |
| `NEW_ACCOUNT_PUBLISH_RATE_LIMIT` | Yes after AN-05 | Successful topics/replies per new-account window; initial value `3` |
| `PUBLISH_RATE_WINDOW` | Yes after AN-05 | Durable account publication window; initial value `10m` |
| `NEW_ACCOUNT_PERIOD` | Yes after AN-05 | Age receiving the stricter publication limit; initial value `24h` |
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
  The standalone Compose stack therefore uses host networking for Caddy and
  the application instead of weakening this rule or trusting a bridge subnet.
  Stack Caddy either derives the client address from its public peer or, when
  loopback-bound behind an existing multi-site TLS edge, consumes the single
  canonical `X-Forwarded-For` value that edge overwrites. Both proxy layers
  delete `Forwarded` and `X-Real-IP`; no arbitrary proxy chain is trusted.
  Stack Caddy overwrites `X-Forwarded-Proto` with the deployment mode's exact
  public scheme so an edge-fed HTTPS request is not mislabeled as inner HTTP.
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
- `ACTIVITY_CURSOR_KEYRING_FILE` is a non-secret absolute clean path no longer
  than 4,096 bytes, containing no NUL. Startup opens it read-only with
  `O_CLOEXEC|O_NOFOLLOW`, validates the opened regular file with `fstat`, reads
  at most 1,025 bytes to distinguish overflow, requires EOF, parses into owned
  immutable memory, and closes the descriptor. It never validates one pathname
  object and later reopens another.
- `ABUSE_RULES_FILE` follows the same absolute-clean-path, `O_CLOEXEC|O_NOFOLLOW`,
  opened-regular-file, bounded-read, EOF, ownership, and close discipline. Its
  maximum is 65,536 bytes. It is not secret, but its path and contents are not
  emitted through logs, diagnostics, health, templates, or artifact metadata.
- Rate counts use canonical unsigned decimal without signs or leading zeroes.
  Request/publication counts are 1 through 100,000; client capacity is 1
  through 65,536. Durations use Go syntax: request and publication windows are
  one second through 24 hours, and the new-account period is one minute through
  30 days. The new-account publication limit must not exceed the established
  limit. The release profile records every exact value; none is inferred from
  an absent environment key.
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
- positive `administration_revision` used only by forum-local administration

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

Migration 000007 also creates `content_renderer_state` under the migration
owner. The packaged runtime-grant artifact idempotently grants the distinct
runtime role only `SELECT` on that table so readiness can observe the completed
renderer transition. Ownership and every mutation privilege remain with the
migration owner; the runtime role receives no renderer-state INSERT, UPDATE, or
DELETE privilege.

### 6.3 `external_identities`

- `user_id` foreign key and unique for version 1.0 single-issuer operation
- `issuer`
- `subject`
- `created_at`, `last_verified_at`
- unique `(issuer, subject)`

Issuer and subject are not user-editable.

### 6.4 `forum_groups` and `forum_group_members`

`forum_groups` contains a stable generated ID, unique bounded name, creator,
timestamps, and a positive administration revision. `forum_group_members`
contains `group_id`, `user_id`, the
administrator that granted membership, and timestamps, with primary key
`(group_id, user_id)` and an index on `(user_id, group_id)`. Role and membership
changes append audit events in the same transaction.

### 6.5 `site_settings`

One boolean-keyed singleton stores the bounded public site name, short
description, closed compiled theme, community-rules source, trusted rendered
HTML, renderer version, positive administration revision, and timestamps. The
runtime may select and update the row but may not insert or delete it. Shell,
rules, and edit queries select distinct projections so rules bodies are not
copied through ordinary page rendering.

### 6.6 `sessions`

- `id`
- `token_hash` unique
- `user_id`
- `issued_at`, `last_seen_at`, `validated_at`, `expires_at`
- `revoked_at` nullable
- bounded `user_agent_hash` and `ip_prefix` or equivalent audit fields

Only a cryptographic hash of the opaque token is stored. Session lookup uses a
constant-time comparison where application comparison is required. Expired and
revoked sessions never authenticate.

### 6.7 `oidc_login_attempts`

- hashed/state lookup key
- protected nonce and PKCE verifier material
- purpose (`login` or `revalidate`) and existing session reference when needed
- `return_path`, structurally constrained by PostgreSQL to an internal browser
  path and validated by the application against the exact configured base path
  and canonical encoding before storage
- `created_at`, `expires_at`, `consumed_at`

State is single use. Cleanup is bounded and safe to repeat.

### 6.8 `areas` and `area_groups`

`areas` includes:

- `id`, unique `slug`, `name`, `description`
- `display_order`
- `visibility`, `posting_mode`
- `created_by`, `updated_by`, timestamps
- positive `administration_revision`

`area_groups` uses primary key `(area_id, group_id)` and foreign keys to the
local group relation.

Constraints:

- Group mappings may exist only for a group-visible area, or are ignored only
  under an explicit migration rule. Prefer rejecting inconsistent state.
- Slugs are normalized, bounded, and immutable after publication unless a
  redirect record is added. Version 1.0 may therefore prohibit slug changes.

### 6.9 `topics`

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

### 6.10 `posts`

- `id`, `topic_id`, `author_id`, `post_number`
- `parent_post_id` nullable only for the first post
- `thread_path` as a one-through-32-element `integer[]` rooted at `1`
- `markdown_source`
- `rendered_html`, `renderer_version`
- `revision`
- `created_at`, `updated_at`, `edited_at`, `deleted_at`
- `deleted_by`, `deletion_reason` nullable
- unique `(topic_id, post_number)`
- unique `(topic_id, id)` supporting the same-topic parent foreign key

Source and rendered sizes have limits. A post edit increments `revision` and
uses `WHERE revision = $expected` to detect lost updates.

`(topic_id, parent_post_id)` references `(topic_id, id)` so a reply cannot name
a post in another topic. Post 1 has no parent and path `{1}`. Every later post
has one parent with a smaller immutable post number and path equal to the
parent path with its own post number appended. Parent and path are immutable;
their validation trigger rejects cycles, inconsistent roots, a depth above 32,
or path drift. `posts_topic_thread_order_idx` orders `(topic_id, thread_path)`
for depth-first page reads. The fixed logical depth bound limits row and index
growth; it does not control CSS indentation.

Migration from alpha.1 adds both columns, backfills each first post as `{1}`
and each existing flat reply as a direct child of `topics.first_post_id` with
path `{1, post_number}`, then validates the tree before enabling its constraints
and index. The migration is additive but the backfill updates existing post
rows and must be timed and backed up before deployment. A documented
compatibility trigger maps a missing parent from the previous alpha.1 binary
to the topic's first post and derives the path, preserving app-only rollback.
Alpha.2 request parsing nevertheless requires an explicit parent and never
relies on that compatibility behavior. Removing the compatibility trigger and
tightening the parent column require a later migration after the alpha.1
rollback window closes.

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
the undeleted topic row for update, locks the addressed parent post, the owning
area row for share, and the area's current group mappings for share. The parent
must be visible, undeleted, in the locked topic, and below the maximum logical
depth before `CanReply` evaluates the locked policy. The held topic lock
serializes allocation from `next_post_number`; one statement inserts the reply
with its immutable parent and derived path and advances `latest_post_id`,
`reply_count`, `next_post_number`, and activity timestamps. The persisted
reply/activity time is the greater of the caller's bounded UTC timestamp and
existing topic activity, so a request that waited on the lock cannot move
chronological post timestamps or area-list activity backward. There is no
automatic retry after an unknown commit outcome.

### 6.11 `topic_reads`

- `user_id`, `topic_id`, `last_read_post_number`, `read_at`
- primary key `(user_id, topic_id)`

Migration 000009 retains the existing positive-number check, adds the validated
`topic_reads_read_at_finite` check over the PostgreSQL microsecond domain, and
adds `posts_topic_unread_visible_idx` on
`(topic_id, post_number) INCLUDE (author_id)` with the exact predicate
`deleted_at IS NULL AND redacted_at IS NULL`. It does not backfill or infer
reads. A legacy nonfinite `read_at` aborts the whole migration transaction and
leaves the migration ledger at 000008 for inspected forward repair.

Only server-owned statements write markers. Updates use `GREATEST`; `read_at`
uses finite database `clock_timestamp()` and changes only when the stored number
advances. Read projections reject a marker above `topics.next_post_number - 1`,
a non-finite time, or any malformed nullable tuple with fixed service failure.
Missing rows are meaningful and are never pre-created in bulk.

### 6.12 Reports and audit

`reports` identifies exactly one supported target type and target ID, records a
bounded reason, workflow status, assignment, and resolution. Report submission
locks the reporter row, rejects a twenty-sixth active report, and relies on the
partial unique indexes to reject a duplicate active report for one target.
Topic and post targets must pass the ordinary access predicate in SQL. A user
target is admitted only when it is not the reporter and has authored a visible,
undeleted post, so the endpoint cannot become an account-enumeration oracle.

The active moderation queue is bounded to 25 rows per page and orders `open`
before `in_review`, then by `(created_at, id)` ascending. It does not return post
bodies. A post-target detail computes the post's current staff-visible tree
page so its fragment link lands on the target rather than assuming page one.
Assignment is self-claim only: `open -> in_review` sets `assigned_to`
to the current staff actor. A moderator may resolve or dismiss only its own
claim; an administrator may finish any in-review report. Terminal states are
irreversible in this release. Resolution and dismissal require a canonical
reason. `report_notes` are append-only, bounded staff notes; adding one is an
audited report action and never changes report state.

`user_warnings` are append-only bounded warning records. Warning a user inserts
the warning and its audit atomically. Muting is a strict transition from no
effective mute to a server-approved expiry of one hour, one day, seven days, or
thirty days. A later or active mute conflicts; expiry clears effective mute
without deleting history. Moderators may warn or mute members; administrators
may act on any other local account. Neither role may act on itself.

Topic pinning is the strict `NULL <-> timestamp` transition. Moving locks the
topic and both source/destination areas, requires a different non-archived
destination visible to staff, and changes only `area_id`; stable topic and post
identifiers remain unchanged. Post hide/restore uses the existing soft-delete
fields and preserves structural tombstones. Redaction is irreversible: it
scrubs canonical Markdown and rendered HTML to a fixed tombstone, records who
redacted it and why, advances revision/edit timestamps, and audits metadata
without copying removed content into the audit log.

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
| `GET` | `/rules` | Current community rules | Visitor |
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
| `POST` | `/topics/{id}/replies/preview` | Preview a parent-addressed reply draft | Member session |
| `POST` | `/topics/{id}/replies` | Create a parent-addressed reply | Eligible member |
| `GET` | `/posts/{id}/edit` | Edit form | Author |
| `POST` | `/posts/{id}/edit/preview` | Preview edit draft | Author |
| `POST` | `/posts/{id}/edit` | Apply edit | Author |
| `POST` | `/posts/{id}/delete` | Author soft delete | Author |
| `POST` | `/reports` | Create report | Member |
| `GET` | `/moderation/reports` | Moderation queue | Moderator |
| `POST` | `/moderation/reports/{id}/claim` | Self-claim open report | Moderator |
| `POST` | `/moderation/reports/{id}/notes` | Append report note | Assigned moderator |
| `POST` | `/moderation/reports/{id}/resolve` | Resolve report | Assigned moderator |
| `POST` | `/moderation/reports/{id}/dismiss` | Dismiss report | Assigned moderator |
| `POST` | `/moderation/actions` | Content/account moderation transition | Moderator |
| `GET` | `/moderation/users/{id}` | Local account status | Moderator |
| `POST` | `/moderation/users/{id}/suspend` | Suspend local account | Moderator |
| `POST` | `/moderation/users/{id}/reinstate` | Reinstate local account | Moderator |
| `GET` | `/admin/areas` | Area management | Administrator |
| `POST` | `/admin/areas` | Create area | Administrator |
| `POST` | `/admin/areas/{id}` | Change area | Administrator |
| `GET` | `/admin/areas/{id}` | Area/group assignment detail | Administrator |
| `POST` | `/admin/areas/{id}/groups/{groupID}` | Grant/revoke one area group | Administrator |
| `GET` | `/admin` | Administration dashboard | Administrator |
| `GET` | `/admin/accounts` | Local-account list | Administrator |
| `GET` | `/admin/accounts/{id}` | Local-account detail | Administrator |
| `POST` | `/admin/accounts/{id}/role` | Change local role | Administrator |
| `POST` | `/admin/accounts/{id}/groups/{groupID}` | Grant/revoke one membership | Administrator |
| `GET` | `/admin/groups` | Local-group list | Administrator |
| `POST` | `/admin/groups` | Create local group | Administrator |
| `POST` | `/admin/groups/{id}` | Rename local group | Administrator |
| `GET` | `/admin/settings` | Site settings form | Administrator |
| `POST` | `/admin/settings` | Update site settings | Administrator |
| `GET` | `/search` | Access-filtered search | Visitor |
| `GET` | `/activity` | Access-filtered recent posts | Visitor |
| `GET` | `/posts/{id}` | Bounded direct post target | Post viewer |
| `GET` | `/topics/{id}/unread` | First eligible unread post redirect | Member |
| `POST` | `/topics/{id}/read` | Mark eligible other-authored head read | Member |
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

Every reply and reply-preview form carries exactly one canonical positive
`parent_post_id`. The visible Reply action on a post supplies that post's ID;
the topic-wide reply action supplies the first-post ID. The value is selection,
not authority: preview may preserve it but cannot probe it, and final
publication locks and reauthorizes the current parent, topic, area, and group
policy before insertion. A malformed, cross-topic, deleted, hidden, missing, or
over-depth parent uses the same generic denial/not-found boundary and never
echoes protected parent metadata.

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
visible conversation nodes in ascending immutable `thread_path` order. The
first post remains the tree root and appears on page 1. A soft-deleted node with
visible descendants remains as a body-free tombstone; a deleted leaf is omitted.
An empty first page is permitted only for a visible topic with no renderable
root, while an empty later page is `404`. PostgreSQL receives a maximum offset
of 249,975 and the fixed limit only.

Topic metadata and the post page come from one access-filtered statement and
one PostgreSQL snapshot. It joins the owning area, applies the complete
server-derived member, staff, and group authority before returning topic text,
and excludes deleted topics and hidden topics for nonstaff. The projection
returns each node's depth, parent ID, safe parent author/permalink metadata,
and whether a deleted node must remain as a structural tombstone. It never
returns deleted Markdown or rendered HTML to an ordinary reader. A left-joined
null post row represents only a visible topic with no renderable posts on page
1; offset pages receive no sentinel and therefore return `404`. The window
count, target post page calculation, and every breadcrumb field come from the
same authorized tree order, so a concurrent policy commit cannot produce
mixed-authority metadata and posts. Topic canonical URLs use
`/topics/<topic-id>` and omit `page=1`. Stable post URLs append
`#post-<post-id>` to that topic page, including the canonical tree-order page
query when required. Breadcrumb area links use the immutable area slug returned
by the same access-controlled result.

Use POST for browser mutations. Method override tricks are not required in
version 1.0. Route bodies, path IDs, query lengths, and pagination sizes are
bounded.

The browser router activates `/login`, `/register`, `/auth/callback`,
`/auth/revalidate`, `/setup`, `/setup/administrator`, and `/logout` as exact
paths. Session lookup wraps only exact routes that can
consume identity: `/`; one-segment `GET /areas/{slug}`; canonical
positive-decimal one-segment `GET /topics/{id}`; the exact publishing,
preview, edit, delete, topic-moderation, account-status, suspend, and reinstate
routes listed above; exact `GET /search` and `GET /activity`; canonical
positive-decimal one-segment `GET /posts/{id}`; canonical positive-decimal
`GET /topics/{id}/unread` and `POST /topics/{id}/read`; setup; revalidation;
and logout. Every numeric identifier must
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
  `private, no-store`; content- or version-addressed CSS and JavaScript are immutable for
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

### 11.1 Alpha.2 forum-surface contract

`ListVisibleAreaSummaries` replaces the plain index projection. Its single SQL
statement returns area identity and display fields plus:

- `topic_count`: actor-visible, non-deleted topics;
- `post_count`: non-deleted posts belonging to those topics;
- an optional latest-post tuple containing topic ID/title, post ID/number,
  author display name, and post creation time.

The complete area visibility predicate appears in the area-producing relation
before aggregation. Non-staff topic visibility excludes `hidden`; staff topic
visibility admits it. Deleted topics and soft-deleted posts are always excluded
from the summary. Counts are exact `bigint` values. The latest-post tuple is
selected by `created_at DESC, id DESC` from the same authorized topic set and
is SQL NULL in its entirety when no visible post exists. The store rejects
negative counts, partial nullable tuples, empty required strings, non-positive
identifiers, or non-finite timestamps.

The handler builds the latest-post URL through `URLBuilder`; templates never
concatenate paths. Board-index output uses semantic row headings and labels
that remain intelligible when desktop statistic columns collapse into mobile
metadata. Topic-list and topic-page changes reuse their existing read models;
new queries are not justified for fields already returned.

Every alpha.2 browser page continues to return a complete document for an
ordinary request and the same `#main-content` component for an exact HTMX
request. Responsive presentation is CSS-only. It introduces no client-side
state, JSON API, authorization branch, copied third-party forum asset, or
runtime stylesheet dependency.

### 11.2 Alpha.2 threaded-reply contract

The topic-post query orders by `(topic_id, thread_path)` and returns at most 25
conversation nodes after the canonical one-based page offset. `depth` is
`cardinality(thread_path) - 1`. The store rejects an empty or non-rooted path,
a last path element different from `post_number`, depth above 32, a partial
parent tuple, or a parent relationship inconsistent with the row's depth. Page
and stable-post URL calculations use the same tree order; chronological latest
activity and unread watermarks continue to use immutable `post_number`.

Each post article owns a child container and a Reply control whose form carries
the post ID as `parent_post_id`. The topic-level Reply control addresses the
first post. A successful HTMX reply returns a same-origin main-region location
for the canonical tree-order page and post fragment. HTMX therefore reloads
one authorized bounded region and places the reply under its parent without a
document refresh. Ordinary submission performs the equivalent `303`. Preview,
validation, reauthentication, and conflict behavior preserve the selected
parent without treating it as authority.

Desktop rendering may show the conventional author/content columns around the
thread, but ancestry is expressed by semantic nesting metadata, a visible
reply-to label, and connector styling. CSS indentation increases for depths one
through six only; deeper nodes retain their true `data-depth`, reply-to label,
DOM order, and accessible relationship while using the sixth-level inset. At
320 CSS pixels the author panel collapses above content before indentation is
applied, and no node may force document-level horizontal scrolling. Published
and previewed sanitized bodies share the exact
`rendered-post min-w-0 max-w-full` containment boundary. Its `pre` and `table`
children become bounded horizontal scrollers, so a wide code line or GFM table
cannot widen the document in either workflow.

An ordinary reader never receives a soft-deleted post body. A deleted leaf is
omitted. A deleted post with a visible descendant produces a tombstone article
with stable post ID, structural parent relationship, fixed “Deleted” wording,
and no source, rendered body, deletion reason, or unauthorized actor metadata.
Editing a post replaces only that article. Deleting or restoring a post
refreshes the bounded topic region so tombstone and descendant structure are
recomputed by the server. Hard-purge tooling must refuse to orphan children or
must remove/reparent an explicitly audited subtree; silent reparenting is not
permitted.

## 12. URL builder

One URL builder owns:

- Normalization and prefixing with `BASE_PATH`.
- Absolute URL construction from `PUBLIC_BASE_URL`.
- Escaping path segments and query values.
- Canonical topic/post URLs.
- OIDC callback and post-logout return URLs.

The builder accepts route components, not arbitrary untrusted URLs. Tests scan
rendered core pages for root-relative application links that omit `/bb`.

## 13. Markdown rendering and authoring

- Canonical input is nonblank UTF-8 Markdown source from 1 through 65,536
  bytes. Sanitized rendered HTML must be nonblank and no larger than 262,144
  bytes before persistence. Alpha.3 does not enlarge that admitted persistence
  or public-page envelope.
- Alpha.3 uses Goldmark v1.8.5 with the four components bundled by GFM:
  tables, strikethrough, task lists, and linkification. They are registered
  explicitly so table alignment output is disabled and linkification accepts
  only `http:` and `https:` URL protocols while preserving GFM's separately
  parsed email autolinks as `mailto:`; user content therefore never requires
  style or alignment attributes and `ftp:` is not promoted to a link. Raw HTML
  and dangerous links retain Goldmark's default disabled behavior. Heading IDs
  and every non-GFM runtime extension stay disabled.
- Link schemes are restricted to an allowlist.
- Rendered HTML passes through a narrow sanitizer allowlist even when the parser
  claims safe output.
- Every surviving link receives `nofollow noreferrer`, avoiding a second
  browser-versus-Go external-URL classification policy.
- The complete p2 element allowlist is `p`, `h1` through `h6`, `hr`, `em`,
  `strong`, `ul`, `ol`, `li`, `a`, `blockquote`, `pre`, `code`, `br`, `table`,
  `thead`, `tbody`, `tr`, `th`, `td`, `del`, and `input`. An `a` may retain
  only its allowed `href`; an `input` survives only with Goldmark's exact empty
  `disabled` and optional empty `checked` attributes plus `type="checkbox"`.
  No heading ID, `name`, `value`, `form`, style, class, event, URL-bearing
  attribute other than the allowed link `href`, or other element/attribute is
  admitted.
- Rendered output carries the exact renderer version
  `goldmark-v1.8.5-gfm-bluemonday-v1.0.27-p2` for deterministic rebuilding.
- Templates receive rendered content through one explicit trusted-HTML type;
  arbitrary strings cannot opt out of escaping.

### 13.1 Renderer migration

- Migration `000007_gfm_renderer.sql` creates one renderer-state singleton for
  the alpha.3 target. Fresh and upgraded databases start incomplete; the
  mandatory release re-render phase must execute its empty completion batch in
  both cases. Redacted tombstones retain the separate immutable
  `moderation-redaction-v1` renderer contract.
- The application is stopped before this migration. A `NOT VALID` check
  constraint immediately rejects obsolete-version inserts and updates without
  first scanning old rows. The schema transaction performs no `posts` scan or
  constraint validation while retaining its table lock. The constraint admits
  the exact current version, an exact `moderation-redaction-v1` row with
  `redacted_at` set, or an ordinary unredacted
  `goldmark-v1.8.5-bluemonday-v1.0.27-p1-preserved` compatibility row.
- Before applying migration 000007, the ordinary argument-free release command
  classifies every existing post in ID order through nullable-cursor batches of
  at most 100 rows. Each batch owns a separate repeatable-read, read-only
  transaction, so retained row data and snapshot lifetime are batch-bounded;
  the complete preflight still performs O(all posts) rendering. One initial
  read-only transaction inspects the schema and migration ledger, followed by
  one read-only transaction and row query per batch plus a final empty batch.
  The initial query and cursor-bearing query are distinct SQL statements: the
  former has no cursor predicate and the latter exposes `post.id > $1`
  directly. A nullable `IS NULL OR` predicate is forbidden because PostgreSQL
  may otherwise select a generic prepared-statement plan that cannot retain the
  primary-key lower bound as an index condition. Transaction and round-trip
  counts therefore grow with population. The required application stop/drain
  makes keyset traversal complete across snapshots and excludes a writer in the
  gap before the later schema transaction; no atomic guarantee is claimed. A
  fresh database with no
  `posts` relation passes. A
  current-p2 marker before the Alpha.3 ledger is impossible and rejected. On
  an idempotent Alpha.3 rerun, every current-p2 source is rendered again and its
  HTML is verified byte-for-byte, every p1-preserved row is revalidated, and
  redaction state is classified before renderer version. Any invalid candidate
  stops before migration 000007 enters the ledger or installs its state or
  constraint.
- After preflight, the release migration command applies schema migrations,
  then repeatedly processes at most 100 stale posts in one transaction ordered
  by post ID. The renderer-state singleton contains a nullable
  `last_processed_post_id`; `NULL` is the distinct initial state before every
  signed 64-bit identity, including `MinInt64`. The command first locks that
  singleton, mechanically serializing multiple migration runners, then selects
  and locks stale rows with `id > last_processed_post_id ORDER BY id LIMIT 100`.
  As in preflight, the initial no-cursor selection and the cursor-bearing
  selection are separate statements; the latter exposes `id > $3` directly so
  a PostgreSQL generic plan preserves the `posts_pkey` lower bound. It advances
  the cursor to the last selected identity and increments the converted count
  in the same transaction as every post update. A rollback
  advances neither posts nor state. A committed transaction whose
  acknowledgement is lost returns an outcome-unknown error; a later invocation
  observes the committed cursor and resumes after it. Rendering uses the same
  `RenderMarkdown` function as preview and publication. If and only if p2
  rendering of an exact application-valid p1 row exceeds the unchanged
  262,144-byte persistence limit, the migration verifies its existing HTML
  byte-for-byte by reconstructing the admitted p1 Goldmark/Bluemonday output,
  preserves that HTML, and records the explicit p1-preserved compatibility
  marker. Valid canonical source whose p2 output fits the limit migrates to p2
  regardless of its obsolete renderer marker because old derived HTML is
  discarded. An unknown renderer becomes fatal only when p2 overflow would
  require exact p1 provenance; invalid source, mismatched p1 HTML, empty output,
  and every non-size render failure also roll back the batch. Runtime create,
  reply, and edit services receive renderer metadata only from the private p2
  rendered-value type and cannot produce the compatibility marker.
- The command observes cancellation before each p2 render and before and after
  the exceptional p1 reconstruction. Cancellation therefore stops before the
  next row and is bounded by one in-progress renderer phase; the transaction
  rolls back rather than publishing a partial batch.
- The final empty batch proves no unhandled stale ordinary post remains, then
  executes PostgreSQL `VALIDATE CONSTRAINT`. That operation scans the complete
  `posts` table and holds `SHARE UPDATE EXCLUSIVE` on it while the transaction
  also retains the renderer-state row lock; neither validation I/O nor lock
  duration is batch-bounded. It then marks the singleton complete in the same
  transaction. Current-p2 and exact p1-preserved rows are both handled.
  An already-recorded completion timestamp is never rewritten. Process
  interruption or an
  unknown commit outcome is recovered by rerunning the command from the
  persisted cursor. The immediately enforced writer constraint prevents an
  obsolete ordinary row from appearing behind that cursor, while the final
  whole-table oracle remains the authoritative completion proof. An edit
  serialized by the row lock either precedes the batch render or persists the
  new p2 renderer itself.
- The re-render loop performs no per-batch output I/O. The renderer-state
  singleton and post renderer markers are the canonical restart/progress
  record, so a blocked logging sink cannot stop database progress or signal
  cancellation. Readiness requires the exact completed target and the
  exact cursor column and progress constraint plus the exact validated writer
  constraint through a constant-shaped catalog query;
  it does not scan the posts table on every probe.
- A measured 100-row dense-task compatibility fixture on the pinned PostgreSQL
  17.10 image took 20.644660958 seconds and the test process peaked at a sampled
  74,368 KiB RSS. This is one deliberately expensive admitted fixture, not a
  universal maximum over all valid p1 source. All 100 selected post rows remain
  locked for that maintenance transaction. This cost is admitted only because
  the application is stopped and drained before migration; it is not an
  online/background workload and the batch limit must not increase without new
  evidence. `docs/verification.md` records the committed reproduction method,
  identities, and result state.
- A separate reproducible population fixture uses 25,000 ordinary posts across
  1,000 full 25-post topics. This is a representative evidence point, not a
  capacity promise. It records complete preflight time and exact batch,
  transaction, and query-round-trip counts, schema time, 250 conversion
  batches, the final validation/completion transaction, total re-render/release
  time, RSS, row count, and exact result state. It also instruments every
  returned mutation identity and forces PostgreSQL generic plans while using
  `EXPLAIN ANALYZE` on both exact preflight and mutation selection statements.
  Each phase performs 251 primary-key selections that return and examine
  exactly 25,000 rows, with every cursor-bearing plan retaining its direct
  `posts_pkey` lower-bound condition rather than rescanning prior prefixes.
  Total maintenance time and I/O grow with population; no universal timing
  bound is claimed.

### 13.2 Native toolbar

- One full-SHA-256-content-addressed same-origin script enhances each
  `[data-markdown-editor]` container. The source textarea remains a normal
  required form control and is usable without the script. The generated
  filename digest must equal its embedded bytes; the old mutable
  `markdown-toolbar-v1.js` route is not served. New HTML therefore selects the
  new immutable object while a rollback selects its prior immutable object.
- Native `button type="button"` controls provide bold, italic, link, block
  quote, inline code, fenced code, ordered list, unordered list, table, task
  list, and strikethrough actions. `aria-label` and `title` describe each
  action; no custom keyboard interception replaces browser tab order.
- Version 1 exposes no Image action. Image authoring is deferred to version 2
  and `gotth-media`, where same-origin upload, scanning, privacy, quota,
  retention, and bounded failure behavior can be specified and implemented as
  one honest media boundary.
- Each enhanced textarea owns composition state from `compositionstart` through
  `compositionend`. Every toolbar activation in that interval is a strict
  content, focus, and selection no-op. The state is local to the editor's
  listener closure, so separate editors and an HTMX-created replacement start
  independently; once composition ends, the existing action path resumes.
  Pointer activation is intercepted before its focus-changing default action;
  per-button, per-pointer state also consumes the same gesture's trailing
  physical click if browser event ordering ends composition before that click.
  A click carrying an integer `pointerId` may inspect and retire only that
  exact pointer's guard; it cannot consume another pointer's state. The
  pointer-less compatibility fallback is used only when the click truly lacks
  an integer pointer identity.
  Cancellation, lost capture, and pointer/mouse release without click retire
  that gesture locally; HTMX cleanup removes any transient document listeners
  before an editor is discarded. Native keyboard activation is identified by
  the browser's zero-detail click and remains the ordinary button path even if
  a canceled pointer lifecycle was interrupted.
- Inline wrappers toggle only when the exact selected/caret-adjacent markers
  match. Bold and italic inspect complete adjacent star runs: applying one to
  the other composes a three-star strong-plus-emphasis delimiter, removing one
  removes only its own width, and unequal exterior authored runs are escaped
  reversibly so a repeated toggle restores those stars byte-for-byte. Line
  actions add/remove prefixes across the complete touched lines.
  Fenced-code and table actions expand to complete touched lines and map the
  original selection into the transformed content. A recognized fence/table
  toggle removes only its actual wrapper/internal syntax and never an exterior
  line ending; fence length and separator-dash count carry no hidden ownership
  state. Production textarea values are browser-normalized to LF. The pure
  helper also preserves uniform CRLF inputs for deterministic testing, but no
  lone-CR or mixed-ending browser behavior is promised. All-space inline-code
  selections omit CommonMark padding so selected spaces remain exact.
  Empty selections insert bounded placeholders and select the useful editable
  portion. Every transformation restores focus and a deterministic selection.
- The client script does not preview, parse, trust, or sanitize Markdown.
  Server preview and publication remain the sole rendering boundary.

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

AN-01 report submission and moderation routes use the same current-session,
revalidation, CSRF, bounded strict-form, server request-UUID, no-retry, and
validated-result boundaries. Submission admits active authenticated users even
when muted, because a publishing restriction must not suppress safety reports;
suspended sessions remain ineligible. Queue reads and all processing require an
active persisted moderator or administrator. The transaction, not the rendered
control, is final authority after concurrent actor, target, or report changes.

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
- Normative CommonMark/GFM examples, disabled task controls, deterministic
  output, malformed/boundary inputs, and exact sanitizer allowlist.
- Native toolbar transformations, caret/selection restoration, multiline and
  repeated-toggle behavior, native semantics, labels/tooltips, focus styling,
  script-failure fallback, full-page/HTMX form replacement, synthetic event
  ordering, and a real Chromium IME pointer/keyboard regression.
- State transition and stale-revision behavior.

### 18.2 PostgreSQL integration tests

- Every access-controlled query against the full access matrix.
- Transaction rollback when audit insertion fails.
- Concurrent reply number allocation.
- Unique identity and login-attempt consumption.
- Topic-read monotonicity.
- Search/count leakage checks.
- Fresh and upgrade migrations.
- Renderer read-only preflight/non-mutation failures, migration batching,
  restart/idempotence, rollback, concurrent edit serialization, completion
  oracle, absence of per-batch output I/O, and readiness failure while stale
  renderer rows remain.

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

## 19. AN-02 search and recent activity

AN-02 is one bounded PostgreSQL read feature. It does not activate
`gotth-search`, add a host lifecycle process, alter publication or Docker
restart semantics, or create a second migration/deployment authority.

### 19.1 Request and response grammar

`GET /search` accepts exactly one each of `q`, `author`, `area`, `from`, `to`,
and `page`. Unknown/duplicate keys, malformed percent encoding, invalid UTF-8,
NUL/control bytes, semicolon separators, or a raw query string over 2,048 bytes
return a fixed bounded `400` before session or database work. At least `q` or
`author` is required; the other filters cannot initiate a search.

- `q` is trimmed with the Unicode 15.0 `White_Space` property, NFC-normalized through the pinned
  `golang.org/x/text/unicode/norm` helper, and 1..256 UTF-8 bytes. PostgreSQL
  alone parses it with
  `websearch_to_tsquery('pg_catalog.simple'::regconfig, $1)`. Empty or
  no-positive-term queries and parsed queries over 31 nodes are rejected before
  candidate discovery using `numnode(parsed_query) BETWEEN 1 AND 31` and
  `querytree(parsed_query) <> 'T'`. Terms, quoted phrases, `OR`, and unary `-`
  are supported; prefix and fuzzy matching are not.
- `author` is a canonical positive decimal int64 user ID, not a display name.
- `area` uses the existing canonical lowercase area-slug grammar.
- `from`/`to` are inclusive UTC `YYYY-MM-DD` dates in years 0001..9999 with
  `from <= to`. The lower predicate is `>= from@00:00:00Z`; ordinary upper
  predicates are `< to+1day@00:00:00Z`; `9999-12-31` uses the exact finite
  microsecond endpoint and never constructs year 10000.
- `page` is canonical positive decimal, defaults to 1, and is at most 2.

Canonical links contain normalized/canonical values, omit absent keys and
`page=1`, and use the shared base-path URL builder and Go URL encoding. Valid
filters remain in the rendered form; fixed errors never quote input.

Search emits at most 25 rows per page from the newest 50 authorized matches.
`50+` means only that a 51st authorized match exists. With `q`, rank is
approximate within the first 50; without `q`, results stay newest-first. Page 1
with no rows is `200`; a valid empty page 2 is `404`. Page 2 uses a fresh
snapshot and therefore may duplicate or omit concurrently changed rows.

Topic results contain readable area/topic/author/time context and no body.
Post results contain readable area/topic/post/author/time context and an
escaped, unhighlighted excerpt comprising the first at most 300 runes of
normalized visible text. They link to the canonical direct post and topic URLs.
All three AN-02 routes render a complete full page or equivalent
`#main-content` HTMX state and set `Cache-Control: private, no-store` on success
and failure.

`GET /activity` accepts either no raw query for the first page or exactly one
nonempty decoded `cursor` key. Unknown/duplicate/empty keys, semicolon
separators, malformed percent encoding, invalid UTF-8, NUL/control bytes, or a
raw query over 256 bytes return the fixed `400` before session or database
work. A terminal page is `200` with no continuation. Canonical first-page links
have no query; continuation links contain only the strict cursor.

`GET /posts/{postID}` accepts one canonical positive decimal int64. One
primary-key-started query joins its topic, area, and author and applies the
complete direct-read predicate before returning fields. Missing, deleted,
redacted, or inaccessible rows are the same fixed `404`. Search projection
health is irrelevant to this direct read. Success renders only that post and
authorized context; it never enumerates parents, descendants, siblings,
totals, or a threaded-page ordinal.
The direct-post route accepts no query string; any query returns fixed `400`
before session or database work.

### 19.2 Authorization-first SQL

The server supplies one validated `policy.AccessContext`; no request field
carries authority. Every discovery branch requires an undeleted topic, permits
hidden topics only for staff, and proves the owning area's public,
authenticated-member, or group visibility. Group visibility uses
`EXISTS` against the complete server-loaded group-ID array; `area_groups` is
never a row-producing outer join. Post discovery also requires undeleted and
unredacted state.

Unauthorized rows contribute no identity, field, rank, count, excerpt, cursor,
or terminality. Those predicates are inside SQL before the 51-row search fence
and the 26-row activity limit. PostgreSQL may still visit index entries, so no
constant-time claim is made.

Search unions typed authorized topic/post identities, applies text and all
filters in each branch, suppresses a root-post result only when that same
topic-title result matches, and orders by `created_at DESC`, topic before post
on an equal timestamp, then typed ID descending. It materializes at most 51;
row 51 is only the sentinel. With text, the first 50 then order by
`ts_rank_cd(search_vector, parsed_query, 32) DESC` followed by the same tie
break. The selected page rechecks authorization and projection identity in the
same read-only repeatable-read transaction. Missing, duplicate, unauthorized,
stale, malformed, or misordered selected data fails the fully buffered response
with fixed `503`; no weaker fill query runs.

Activity is post activity ordered by `(posts.created_at DESC, posts.id DESC)`.
A continuation adds the strict tuple predicate `< (cursor_created_at,
cursor_post_id)`. Authorization and current projection are before `LIMIT 26`;
25 rows are returned and row 25's boundary is emitted only when row 26 exists.
Restricted rows therefore cannot suppress older public activity or reveal
restricted occupancy. There is no exact total.

### 19.3 Activity cursor and keyring

The cursor is strict unpadded base64url over this exact 77-byte record, yielding
exactly 103 ASCII characters:

```text
version(1) | key_id(uint32) | issued_at_unix_seconds(int64)
| created_at_unix_microseconds(int64) | post_id(int64)
| audience_digest(16) | HMAC-SHA-256(32)
```

Integers are big-endian; signed fields use two's complement. Version is 1,
key/post IDs are positive, and the occurrence time must round-trip within
0001-01-01T00:00:00Z through 9999-12-31T23:59:59.999999Z. The outer HMAC covers
the exact ASCII domain string `gotth-bb/activity-cursor/v1` and all preceding
bytes. Decode enforces exact length,
alphabet, no padding, canonical re-encoding, known active/previous key ID, and
constant-time MAC comparison before session/activity-query work.

The 16-byte audience digest is the prefix of a separate HMAC under that cursor
key, beginning with exact ASCII `gotth-bb/activity-audience/v1`. Its remaining
input is: format byte `1`; authentication byte (`0`/`1`);
big-endian signed user ID (zero only anonymous); role byte (`0` anonymous, `1`
member, `2` moderator, `3` administrator); then every complete sorted unique
positive group ID as a big-endian signed integer. The fixed-width remainder is
unambiguous and is streamed over the server-loaded slice. An authenticated
slice that is not strictly increasing and positive fails fixed `503` rather
than being sorted or deduplicated into a second authority snapshot. A mismatch after
optional-session resolution is the same fixed `400` as malformed/expired and
runs no activity candidate query.

One statement at the start of the activity transaction samples finite
PostgreSQL `clock_timestamp()` and decides cursor/key time validity before
candidate SQL. Issue time over 60 seconds in the future or age over 24 hours is
invalid. A new cursor uses that database sample; process-local time state is not
authority.

Startup reads one immutable cursor-keyring secret. Its UTF-8 JSON is at most
1,024 bytes and contains exactly `version: 1`, one `active` object, and
`previous: null` or one previous object. Each object has only a distinct
nonzero uint32 decimal `id`, strict unpadded-base64url 32-byte `key`, and UTC
RFC3339-second `not_before`/`issue_not_after` with ordered bounds. Bounds plus
24 hours and 60 seconds fit years 0001..9999. Duplicate/unknown fields, trailing
JSON, symlinks, non-regular files, runtime write permission, or group/world
write bits fail startup without echoing content. The read-only mount/service
credentials follow existing secret policy.

Emission uses active only inside its inclusive issuance window. Decode also
requires `issued_at` inside the selected key's window; previous accepts only
through its `issue_not_after + 24h + 60s`. A valid but non-issuing active key
makes `/activity` fixed `503` while global readiness, search, and unrelated
routes remain available.

### 19.4 Projection schema and writers

`SearchProjectionVersion` is the exact compiled literal
`search-v1-pg17-simple-u15-p2`. It binds renderer
`goldmark-v1.8.5-gfm-bluemonday-v1.0.27-p2`, the renderer-owned visible-text
algorithm, pinned NFC/Unicode-15 identity, PostgreSQL major 17, and
`pg_catalog.simple`. Any output-affecting change requires a new literal and
complete rebuild. Before a PostgreSQL 17 minor update, the stopped preflight
recomputes every vector on the candidate server and compares byte-for-byte;
any difference requires a new literal/rebuild.

Topic vector input is the stored title after NFC normalization. The renderer
derives sanitized trusted HTML and normalized visible text from one parse of
sanitized output. It emits only text nodes in render order, inserts one ASCII
space at boundaries for `p`, `h1` through `h6`, `hr`, `ul`, `ol`, `li`,
`blockquote`, `pre`, `br`, `table`, `thead`, `tbody`, `tr`, `th`, and `td`,
collapses Unicode 15.0 `White_Space` runs to one ASCII space, trims,
NFC-normalizes,
and emits no markup or attributes. Redacted input is empty. Search query text
uses the same NFC helper.

Migration 000008 adds topic/post `search_vector tsvector` and
`search_projection_version text`; a boolean-true-primary-key
`search_projection_state` with exact target, closed topics/posts/complete
phase, nullable signed-bigint cursor, separate nonnegative converted counts,
and nullable completion time; and six `NOT VALID` checks covering the two
projection tuples, two finite created-at domains, and two positive-ID domains.
Historical projection tuples are all NULL; current tuples have non-NULL vector
and the exact version. The checks are named
`topics_search_projection_current`, `posts_search_projection_current`,
`topics_search_created_finite`, `posts_search_created_finite`,
`topics_search_id_positive`, and `posts_search_id_positive`. Completion
validates all six.

The singleton row is exactly `singleton=true` and the target literal. Its
cursor is NULL or positive. In `topics`, post count is zero, completion is NULL,
and topic count is zero exactly when the cursor is NULL. Transition to `posts`
resets cursor to NULL; in `posts`, completion is NULL and post count is zero
exactly when the cursor is NULL. In `complete`, completion is a finite UTC
microsecond value and post count is zero exactly when the retained final-post
cursor is NULL. `search_projection_state_target_current`,
`search_projection_state_shape`, and
`search_projection_state_completion_finite` enforce the target, relational
shape, and time domain. Any other phase/count/cursor/time tuple is rejected by
the database and readiness.

The five exact partial indexes are:

- `topics_search_vector_current_idx`: GIN `topics(search_vector)`;
- `posts_search_vector_current_idx`: GIN `posts(search_vector)`;
- `topics_search_author_current_idx`: B-tree
  `topics(author_id, created_at DESC, id DESC)`;
- `posts_search_author_current_idx`: the same columns on posts; and
- `posts_activity_current_idx`: B-tree
  `posts(created_at DESC, id DESC)`.

Every topic predicate is `deleted_at IS NULL AND
search_projection_version = 'search-v1-pg17-simple-u15-p2'`. Every post
predicate adds `redacted_at IS NULL`. Query predicates match syntactically.
No row quota, stored full visible-text copy, exact global count, or index hint
is added.

Publish/edit/redact derive vector input at the renderer boundary and persist
content, exact projection version, and
`to_tsvector('pg_catalog.simple', $visible_text)` atomically. Redaction writes
the exact empty vector/current version. Delete/hide/move need not rewrite vector
bytes because reads exclude invisible state.

Migration 000008 leaves `gotth_validate_topic_post_state()` unchanged but
replaces its two all-column constraint triggers with six. The exact names are
`topics_validate_post_state_insert`, `topics_validate_post_state_update`,
`topics_validate_post_state_delete`, `posts_validate_topic_state_insert`,
`posts_validate_topic_state_update`, and
`posts_validate_topic_state_delete`. Topic/post INSERT and DELETE retain
deferred checks. Topic UPDATE names only `id, first_post_id, latest_post_id,
reply_count, next_post_number`; post UPDATE names only `id, topic_id,
post_number`. Each UPDATE also has an exact `OLD ... IS DISTINCT FROM NEW ...`
disjunction, so projection-only writes do not queue whole-topic validation.
Completion/readiness attest those definitions and the unchanged function.

### 19.5 Migration, readiness, and rollback

The application and all writers are stopped/drained before the ordinary
argument-free migration command. Its complete read-only preflight walks topic
and post primary keys in batches of at most 100, validates positive IDs/finite
times, derives exact projection input, asks PostgreSQL 17 to construct each
vector, and reports only kind, ID, and fixed error class. The preflight is not a
concurrency barrier; stopped-writer state is mandatory and rechecked before
schema apply.

The existing advisory migration lock covers migration 000008 and its ledger row
in one transaction. Nullable columns do not rewrite heaps. The five initially
empty partial indexes still perform heap passes to evaluate predicates; release
evidence measures their I/O/lock exposure. After commit, batches of at most 100
lock the singleton and selected rows, write vectors and phase/cursor/count in
one transaction, and resume solely from database state. Moving topics to posts
resets the cursor. Failure advances nothing.

After the post selection is empty and before completion, the command runs
`ANALYZE public.topics` and `ANALYZE public.posts` as explicit separate
statements. A crash before completion repeats both safely on rerun. Competing
runners may duplicate analysis work but cannot skip it or mark completion
before one successful pair; the stopped maintenance window and admission
measurements account for that bounded concurrency case. Readiness never relies
on eventual autovacuum to make first-service plans usable.

Completion proves zero NULL/partial/stale tuples, validates all six checks,
attests exact indexes and six narrowed triggers plus unchanged function, and
reconciles both converted counts with exact table row counts and the retained
post cursor with `max(posts.id)` (both NULL only for no posts). It then marks
complete once while preserving first completion time on rerun. The
zero-stale oracle and `VALIDATE CONSTRAINT` work scan complete affected tables;
validation takes PostgreSQL `SHARE UPDATE EXCLUSIVE` locks. Completion I/O,
lock duration, and total time are population-dependent and not batch-bounded.
Startup
and `/health/ready` require exact migration head 000008, the one complete
current singleton, exact validated checks/indexes/triggers/function, and the
admitted Alpha.3 renderer. Startup also requires a structurally valid keyring;
active issuance expiry remains route-local. Each query still requires current
row projection.

Before 000008, restore the prior artifact normally. After commit, its
exact-migration readiness fails; recover by forward repair/current artifact or
the existing verified pre-migration backup/restore. Unknown COMMIT outcome is
resolved from the ledger/schema before retry. AN-02 adds no down migration or
physical-backup system.

### 19.6 Resource, failure, and logging contract

Search/activity share a process-local non-waiting semaphore of capacity two.
Saturation returns fixed `503` plus `Retry-After: 1` before session/pool work.
Search has one five-second application context and activity two seconds,
covering session/group load, repeatable-read checkout, semantic parse, SQL,
validation, excerpt derivation, and bounded rendering. Each transaction uses a
statement timeout no longer than that context, `lock_timeout=250ms`,
`work_mem=4MB`, parallel gather off, and JIT off. These schedule cancellation;
they are not hard resource guarantees.

Rendering completes before headers. Transaction/checkout end before network
output. The permit stays held until the bounded write ends, so at most two
discovery buffers/slow writes exist per process; the existing 30-second server
write deadline is the outer slow-client bound. Pages contain at most 25 rows
and the full envelope is at most 256 KiB. Overflow, cancellation, SQL, row,
projection, or template failure discards uncommitted output and returns fixed
`503`. There is no in-request retry or partial authorized response.

Application logs/metrics retain route pattern, fixed outcome, status, duration,
and bounded counts only—not filters, cursor, identity, fields, snippets, or raw
body. Caddy still receives the raw query-bearing request target, and enabled
PostgreSQL statement/parameter diagnostics may see bound values. Those are
separate operator-controlled trust boundaries and are never hidden by an
application-log redaction claim.

## 20. AN-03 unread state

AN-03 is one PostgreSQL-backed signed-in preference feature. It adds no cookie,
cursor, external service, scheduled cleanup, background job, notification, or
anonymous tracking state.

### 20.1 State and visible semantics

For one authorized signed-in actor and topic, `read_head` is the greatest
`post_number` whose row has `deleted_at IS NULL`, `redacted_at IS NULL`, and
`author_id <> actor.user_id`. If no such eligible row exists, the topic has no
readable other-authored head and its state is `read` regardless of marker
absence. A member's own posts never create that member's unread state.

The three closed UI states are:

- `new`: an eligible head exists and no `(user_id, topic_id)` marker exists;
- `unread`: a structurally valid marker exists and is less than `read_head`;
- `read`: the marker is at least `read_head`, or no eligible head exists.

`new` and `unread` both contribute one to the board index's exact
`unread_topic_count`; a topic contributes at most once. Visitors receive no
state enum, marker, read time, count, first-unread link, or mark-read form.
`read_at` is private implementation state and is not rendered, logged, or
returned by any public read model.

Marker numbers remain chronological high-water acknowledgments, not proof that
the user viewed every tree-ordered node. Topic/post/search/activity GETs and
the first-unread GET never mutate state. There is no decrement, mark-unread,
per-page bitmap, global unread feed, or unread-post total in version 1.0.

### 20.2 Authorization-first read models

The authenticated board-index statement extends its existing
`visible_areas -> visible_topics` relations before joining the current user's
marker or testing readable posts. It returns exact nonnegative
`unread_topic_count` per area. The visitor statement remains non-personalized
and does not touch `topic_reads`.

The existing 25-topic area page returns one closed read state per authorized
topic. Its authorization relation precedes the marker join and the readable-
post existence test. Both statements use only the server-loaded positive user
ID, role, and complete sorted group set; request fields never carry authority.
Group areas retain the `EXISTS` semi-join and cannot multiply a topic or count.
Authenticated board-index, area-topic, and topic-page success and failure
responses set `Cache-Control: private, no-store`. Visitor statements and pages
retain the existing non-personalized cache contract. No shared anonymous cache
stores marker-derived output.

The store validates positive IDs and marker numbers, finite `read_at`, marker
at most `next_post_number - 1`, the closed state enum, nonnegative counts, and
state/head/marker consistency. Any partial, duplicate, contradictory, or
malformed result fails the buffered response with fixed `503`. Unauthorized
topics contribute no identity, marker existence, count, state, target, page,
or terminality.

### 20.3 Mark-read writes

`POST /topics/{topicID}/read` accepts one canonical positive decimal int64 path
segment and no raw query. A noncanonical path remains the generic fixed `404`;
any query is fixed `400`. Both reject before session lookup, body read, CSRF
validation, or database work. The route next authenticates. A missing current
session redirects to login and a stale session redirects to revalidation,
without reading the body or probing the topic. A current local authenticated
member/staff session then preserves the existing CSRF grammar: exactly one
`X-CSRF-Token` header is validated without reading a body, or a bounded
`application/x-www-form-urlencoded` body contains exactly one `_csrf` field.
After header validation, the route-specific body parser requires an empty body;
after form validation, it requires exactly that one `_csrf` field and no other
field. The wire bound is 4,096 bytes. CSRF failure is fixed `403`; strict body
failure is fixed `400`; neither performs database work or reflects input. The
route never accepts a watermark, user ID, role, group, return URL, or topic
field. Every outcome is `Cache-Control: private, no-store`.

One transaction applies the complete direct-topic authorization predicate,
selects the greatest currently readable post number through
`posts_topic_unread_visible_idx`, excluding `author_id = actor.user_id`, and
upserts the current actor's row. A topic with no eligible other-authored post is
a successful no-op that creates no marker. Conflict update sets
`last_read_post_number = GREATEST(old, selected)` and changes `read_at` to the
same finite database sample only under `WHERE selected > old`; an equal or
lower boundary performs no row update. A retry is idempotent.
Missing, deleted, hidden, or inaccessible topics are fixed `404`; stale
authentication follows the existing revalidation redirect; CSRF or strict-form
failure performs no database work; database/cancellation failure is fixed
`503` with no partial response.

Ordinary success is an empty `303` to the builder-owned canonical topic root.
HTMX success is an empty `204` with the equivalent same-origin `HX-Location`
JSON object containing the canonical topic root, `target: "#main-content"`, and
`swap: "outerHTML"`; HTMX fetches that root, swaps the main region, and pushes
the canonical URL. The form is shown only for a `new` or `unread` topic, but the
transaction remains authoritative. This preference write appends no moderation
audit row. Topic/reply publication, edit, preview, delete, restore, redact,
moderation, and move paths do not write read state.

### 20.4 First-unread navigation

`GET /topics/{topicID}/unread` accepts the same canonical ID grammar and no raw
query. A noncanonical path remains the generic fixed `404`; any query is fixed
`400`. Both reject before session, body, or database work. A missing current
member session redirects through the existing login return
path without probing the topic. Stale authentication follows the existing
revalidation path.

One read-only statement first produces the authorized undeleted topic, joins
the current actor marker, computes the eligible other-authored `read_head`, then
chooses the lowest undeleted/unredacted other-authored `post_number` greater
than `COALESCE(marker, 0)`. It always returns one authorized-topic sentinel with
the complete marker/read-head tuple, even when no target exists, so malformed
persisted state cannot collapse into a no-unread redirect. After choosing a
target, it materializes at most the first 250,001 renderable identities in the
exact actor-visible tombstone rules and `thread_path` order of
`GetVisibleTopicPostPage` to locate that target. No unread target yields empty
`303` to the canonical topic root. An
ordinal from 1 through 250,000 yields page
`((ordinal - 1) / 25) + 1` and the canonical 25-node topic-page URL with
`#post-<id>`, omitting `page=1`. A target not found inside that bound
uses the canonical `/posts/<id>` direct-post route. Absence from the bounded
identity set selects that fallback; the statement does not scan an unbounded
tree to prove absence and does not widen topic pagination.

The statement returns no body/source and validates topic/post identity,
positive target number, ordinal bounds, and deterministic target selection.
Missing, deleted, hidden, or inaccessible topics are fixed `404`; SQL,
cancellation, malformed rows, or URL construction failure is fixed `503`.
Every outcome is `private, no-store`. A successful ordinary request returns an
empty `303` with builder-owned `Location`. A successful HTMX request returns an
empty `204` with the existing same-origin `HX-Location` JSON object containing
the same canonical path and fragment, `target: "#main-content"`, and
`swap: "outerHTML"`; HTMX fetches the destination, swaps the main region, and
pushes that canonical URL. `HX-Redirect` remains reserved for login and
revalidation session boundaries.

### 20.5 Deletion, revocation, and concurrency

Soft-deleted, redacted, or current-actor-authored posts do not create unread
state. If such a position was the only unread one, the indicator may disappear
without advancing the marker. Restoration of another author's post above the
marker becomes unread; restoration at or below it remains read. Hard post
purge does not lower a marker. Topic soft deletion, hiding, area-policy change,
group removal, or suspension makes the marker
non-authoritative and invisible but does not delete it. Suspension retains the
existing immediate active-session failure, so subsequent public reads use
visitor semantics and no mark-read authority. Mute does not hide state or the
mark-read control while the session retains direct read authorization.
Topic/user hard delete uses the existing cascading foreign keys.

A post committed after a read-model snapshot may appear on the next request.
A mark-read transaction selects its own server boundary; a later eligible post
remains unread. Concurrent mark-read requests serialize only on the one marker
key and converge through `GREATEST`; publication never locks or writes that
marker. Marker state never grants topic access and cannot make a stale session
more authoritative.

### 20.6 Migration, resources, logging, and rollback

Migration 000009 performs exactly two schema changes: add
`topic_reads_read_at_finite` as `NOT VALID` then validate it, and create
`posts_topic_unread_visible_idx` as a regular partial B-tree on
`(topic_id, post_number) INCLUDE (author_id)` for
`deleted_at IS NULL AND redacted_at IS NULL`.
It has no data rewrite, backfill, readiness singleton, or custom runner. Index
creation scans `posts` and may block concurrent writers; constraint validation
scans `topic_reads` under PostgreSQL's validation lock. Release evidence records
their elapsed time, relation size, locks, and I/O on the representative corpus.
A legacy `infinity` or `-infinity` `read_at` makes validation fail and aborts the
whole transaction, leaving the ledger at 000008. The operator inspects and uses
a reviewed forward repair before retry; migration never silently rewrites,
deletes, or fabricates a marker.

Area pages remain fixed at 25 topics. First-unread ordinal work materializes at
most 250,001 renderable identities and returns one target. Mark-read returns no
content and writes at most one marker row, but its eligible-head scan is not
constant work. Authenticated board/area reads and first-unread have one
five-second application context; mark-read has one two-second context. Each
statement sets a transaction-local `statement_timeout` no longer than its
context and `lock_timeout=250ms`; these schedule cancellation rather than
guaranteeing hard latency. Board counts and actor-excluding post scans remain
population-dependent, including the own-post-only worst case. No universal
latency claim is made. New SQL is cancellation-safe and runs without an
in-request retry.

Application logs retain route pattern, fixed outcome, status, duration, and
bounded row/count metrics only. They exclude user/topic IDs, marker existence,
marker number, read time, target post, and redirect location. PostgreSQL
diagnostic logging remains a separate operator-controlled boundary.

Readiness requires exact migration head 000009 through the existing migration
ledger check; there is no additional health state. Before 000009, use the prior
artifact. After it commits, exact-head readiness prevents the prior artifact
from starting; recovery is forward repair/current artifact or the existing
verified pre-000009 restore. No down-migration or inferred rollback is claimed.

## 21. AN-04 administration completion

AN-04 completes the existing forum-local administration surfaces. It does not
add an Authentik client with administrative scope, local credentials, account
creation, impersonation, bulk mutation, arbitrary branding code, or a second
authorization system.

### 21.1 Migration 000010 and site settings

Migration `000010_administration_completion.sql` performs these changes in one
ordinary migration transaction:

- create exactly one `public.site_settings` row keyed by `singleton boolean`
  constrained to true;
- store `site_name text`, `site_description text`, `brand_theme text`,
  `rules_markdown text`, `rules_html text`, `rules_renderer_version text`,
  positive `administration_revision bigint`, and finite `updated_at timestamptz`;
- add positive `administration_revision bigint NOT NULL DEFAULT 1` columns to
  `users`, `forum_groups`, and `areas`; application mutations increment them
  atomically and reject overflow;
- bound the site name to 1–80 Unicode scalar values, the description to 0–280,
  rules source to 65,536 bytes, and rules HTML to 262,144 bytes; reject name/
  description controls in database checks where PostgreSQL can express the
  rule and enforce strict UTF-8/NFC for those two fields at the application
  boundary; preserve nonempty rules source byte-for-byte under the exact
  admitted Markdown-source boundary rather than normalizing it; close themes
  to `blue`, `cyan`, `emerald`, `amber`, or `rose` and the renderer version to
  exact `goldmark-v1.8.5-gfm-bluemonday-v1.0.27-p2`;
- require `rules_markdown` and `rules_html` to be either both exact empty
  strings or both nonempty, making the empty-rules state a closed sentinel;
- seed the current presentation (`GOTTH Board`, `Community discussions, plainly
  organized.`, `blue`, and empty source/HTML under the exact current renderer)
  so upgrade does not silently rebrand the site;
- add nullable `target_site boolean` to `moderation_actions`, constrain a
  present value to true, add `site` to the closed target types, and extend the
  exact-one-target rule accordingly; and
- add `create_group`, `rename_group`, `grant_area_group`,
  `revoke_area_group`, and `update_site_settings` to the closed audit actions.
  Existing `change_role`, `grant_group_membership`,
  `revoke_group_membership`, and area actions remain unchanged; and
- replace the audit reason-shape check so every non-NULL reason has an
  `octet_length` of 1–2,000 bytes, has no POSIX control character, and equals
  its PostgreSQL ASCII-space trim; and
- extend the audit reason-required check to `change_role`, both membership
  actions, `create_area`, `update_area`, both area-group actions, both group
  lifecycle actions, `reinstate_user`, and `update_site_settings`.

The migration adds no content/account backfill. Dropping and replacing audit
checks and validating the settings row take the documented PostgreSQL table
locks and scans; evidence records them instead of calling the change free.
Fresh and upgrade paths finish at exact head 000010. A failed or unknown
transaction is inspected through the migration ledger and catalog before any
retry. There is no down migration; rollback is the prior artifact before 000010
or forward repair/current artifact after it commits.

Administration mutations still set finite `updated_at` for operator context,
but that timestamp is informational and never serves as authority or an
optimistic token.

Readiness extends exact-head verification with catalog attestation for the
singleton relation, all four revision columns and their positive checks,
column types/defaults/nullability, audit target/action constraints, runtime
grant delta, exact one-row cardinality, finite time, closed theme, and current
rules-renderer tuple. The packaged grant artifact preserves the pre-AN-04
runtime baseline and adds exactly:

- `SELECT` plus column-level `UPDATE(site_name, site_description, brand_theme,
  rules_markdown, rules_html, rules_renderer_version,
  administration_revision, updated_at)` on `site_settings`, with no INSERT,
  DELETE, or key update;
- `SELECT`, column-level `INSERT(name, created_by, created_at, updated_at)`, and
  column-level `UPDATE(name, updated_at, administration_revision)` on
  `forum_groups`;
- `USAGE, SELECT` on `forum_groups_id_seq`; and
- `SELECT`, column-level `INSERT(group_id, user_id, granted_by, created_at)`,
  and DELETE on `forum_group_members`, with no UPDATE.

Existing area administration already requires mapping INSERT/DELETE on
`area_groups`; AN-04 does not relabel that baseline as a new grant. Readiness
attests the required delta and rejects INSERT/DELETE/key-update on settings,
DELETE on users/groups/areas/audit rows, UPDATE on either mapping relation, or
missing group-sequence privileges. It does not claim an additive artifact can
erase or globally attest unrelated operator-managed grants outside the required
delta and explicit forbidden-operation set.
Missing, duplicate, malformed, or stale settings fail readiness and page
rendering closed. The application does not invent an in-memory fallback.

### 21.2 Site presentation, settings mutation, and public rules

Every AN-04 mutation reason uses the existing strict nonblank, control-free,
single-line 1–2,000-byte boundary. The application rejects it before a
transaction. The database applies the byte/control/ASCII-trim defense above
to every present audit reason and requires a non-NULL reason for every AN-04
audit action, including the reinstatement action exposed from account detail.
The application remains responsible for strict UTF-8 and Unicode-space
semantics that PostgreSQL's locale-sensitive character classes cannot honestly
duplicate; reason text does not gain an NFC requirement.

Unless a narrower section says otherwise, every AN-04 writer uses read
committed isolation, sets transaction-local `statement_timeout` to no more
than two seconds and `lock_timeout` to 250 milliseconds before acquiring its
first application lock, and uses the request context for every statement. A
canceled or timed-out transaction rolls back. A commit error is an unknown
outcome: the service returns that uncertainty, performs no detached work or
automatic retry, and requires state/audit inspection before an explicit retry.

`SiteShellPresentation` contains only name, description, and closed theme. One
primary-key query loads it after exact route/query preflight and, for protected
routes, after the session/role decision establishes that a full document will
be rendered. It may surround successful or failed domain-data work but never
runs before protected authorization, for a redirect, or for an HTMX fragment.
This preserves branded validation/domain errors without moving private queries
ahead of authority. Static assets, health, readiness, and fixed plain settings-
unavailable responses do not perform this query.

The complete-document footer renders the literal accessible link
`Powered by GOTTH Board` with exact `href="https://github.com/gotthboard"`.
Neither `SiteShellPresentation.Name` nor `pageView.HomeURL` participates in
that link. The configurable site identity continues to drive the document
title, masthead, and home navigation; changing it must not relabel or retarget
software attribution. Fragment rendering performs no footer work.

Area administration uses explicit field-class literals within
`administration_completion.templ`: every interactive control is `w-full` and
`min-w-0`, has a visible border/background/foreground, and receives an
unambiguous focus-visible ring. Labels are grid containers with stable gaps.
Slug/name and numeric/policy controls stack at the base width and enter two-
or three-column grids only at `sm`; description remains full-width. The create
surface alone exposes the optional initial-group identifier. The detail
surface manages group assignment through its existing named group cards.
Native field names, allowed values, byte limits, and POST routes do not change.

If shell loading fails for an otherwise-successful full document, the handler
returns fixed bounded unbranded `503`. If authorization or domain work already
established a non-2xx status, shell failure preserves that status and replaces
only the body with fixed bounded unbranded text. It never turns fixed `403` or
`404` terminal behavior into a settings-availability oracle.

`PublicRules` is a separate primary-key projection containing the three shell
fields plus trusted rules HTML. `EditableSiteSettings` is a third
administrator-only projection containing those shell fields plus source,
renderer metadata, and the positive numeric revision. Each route uses that one
row for both its document shell and main content; it does not issue a duplicate
shell query. Ordinary shell queries never select rules source or HTML. There is
no process-local cache, notification channel, or propagation claim.

The public `GET /rules` accepts the exact canonical path and no query, is
read-only, renders the current sanitized rules, and returns a fixed unavailable
response when the row is malformed or unavailable. Empty rules render an
explicit no-rules message. The response exposes no update time, actor, audit
reason, raw Markdown, or renderer metadata. Every outcome is `no-store`, so an
intermediary cannot contradict the next-read propagation contract.

The administrator settings form accepts exactly `_csrf`, `site_name`,
`site_description`, `brand_theme`, `rules_markdown`, `reason`, and `revision`.
The form body is at most 256 KiB, which admits the worst-case form encoding of
the 65,536-byte rules source plus the bounded scalar fields. Text is strict
UTF-8; site name and description are NFC/control-free and have no surrounding
whitespace where their field contract forbids it. Rules source is exact empty
or is accepted byte-for-byte by the admitted `render.RenderMarkdown` boundary;
the settings layer neither normalizes it nor invents a stricter character
policy. `revision` is the canonical positive decimal administration revision
from the database. Before opening a transaction, the service validates the
closed theme. Exact empty rules source produces the exact empty HTML sentinel
with the current renderer version; every nonempty source renders/sanitizes
through that boundary. No other path manufactures trusted rules HTML.

The transaction sets `statement_timeout` to at most two seconds and
`lock_timeout` to 250 milliseconds, locks and revalidates the current
unsuspended administrator row, locks the settings singleton, compares the
exact revision, rejects a no-op or revision overflow, updates every settings
field and renderer tuple, increments the revision by one, and appends one
`update_site_settings` audit. Previous/resulting audit objects contain only the
bounded name, description, theme, renderer version, and SHA-256 digests of the
rules source and HTML; rules bodies never enter the audit row's 16-KiB JSON
boundary. Digests are lowercase hexadecimal SHA-256 of the exact stored UTF-8
bytes. The row uses `target_type=site` and `target_site=true`. Rendering or
audit failure commits nothing. Commit failure is unknown
and is not retried automatically.

Themes map only to static compiled selectors on a closed `data-brand-theme`
attribute. No settings field becomes CSS, HTML, JavaScript, URL, asset path,
CSP source, or template name.

### 21.3 Bounded account and group projections

Every private administration page store query begins with one `actor AS
MATERIALIZED` CTE that selects the exact current user only when its persisted
role is `administrator` and its suspension is not effective at the query's one
database time. Every target, group, area, mapping, or aggregate CTE depends on
that row through an inner join or lateral input; no private relation is
referenced by an independent sibling CTE. An absent actor row yields the fixed
denial without target existence, cardinality, continuation, or timing detail.
The retained custom/generic JSON plans must show the actor fence before private
work; application call order is not accepted as a substitute.

`GET /admin/accounts` accepts either no query or exactly one canonical positive
`after` user ID. The authorization-first query rechecks the current actor as an
unsuspended administrator before producing at most 51 users ordered by ID; the
handler renders 50 and uses only row 51 as the next sentinel. Returned columns
are ID, display name, closed role, effective suspended boolean, finite creation
and update times, and no external-identity/session/profile-contact fields.

`GET /admin/accounts/{userID}` returns the same account state, positive
administration revision, and the already admitted moderation status needed for
suspend/reinstate controls. It does not load a population-sized membership set.
An optional exact `groups_after` positive group ID pages a separate
authorization-first projection of at most 51 groups ordered by ID, each with a
nonmultiplying membership boolean; the handler renders 50 and row 51 is only a
next sentinel. Reaching every membership requires paging, not an installation-
wide quota.

Group names are NFC/control-free, 1–80 Unicode scalar values, trimmed, and
case-insensitively unique. `GET /admin/groups` accepts no query or exactly one
canonical positive `after` ID and returns at most 51 groups ordered by ID;
the handler renders 50 and row 51 is only a next sentinel. Create accepts
exactly `_csrf`, `name`, and `reason`. Rename takes its sole group ID from the
canonical path and accepts exactly `_csrf`, `name`, `reason`, and the positive
numeric `revision`. Neither body accepts a target ID. Both revalidate the actor
in the transaction by locking the actor row before inserting or locking the
group, reject no-op/stale/overflow state, increment the group administration
revision on rename, and append one immutable group-target audit. Groups are not
deleted in version 1.0.

A group-membership mutation targets one canonical positive account ID and one
canonical positive group ID and accepts exactly `_csrf`, closed `action`
(`grant` or `revoke`), `reason`, and the target account's positive numeric
revision. The transaction locks actor and target users in ascending ID order,
then the one group; revalidates the active administrator, active target, group,
and revision; rejects a no-op or revision overflow; changes exactly one
mapping; increments the target administration revision; and appends exactly one
matching grant/revoke audit under the request ID. That audit targets the user
and stores the one group ID plus previous/resulting membership boolean in
bounded state. Any mapping,
revision, or audit failure rolls back the whole mutation. Local group access
changes on the next protected request because session authentication reloads
memberships.

### 21.4 Role and suspension governance

The role route takes its sole target user ID from the canonical path and
accepts exactly `_csrf`, closed `role`, closed `expected_role`, positive
numeric `revision`, and `reason`; no target ID is accepted in the body. `role`
is exactly one of `member`, `moderator`, or `administrator`, `expected_role`
is the target's currently rendered closed role, and `reason` uses the common
1–2,000-byte single-line boundary. The role transaction uses the common read-
committed isolation and timeout/unknown-outcome contract in this order:

1. lock the governance singleton;
2. lock actor and target users in ascending positive ID order;
3. revalidate the current unsuspended administrator and exact distinct target;
4. reject stale revision, stale expected role, no-op, malformed persisted state,
   revision overflow, or an effectively suspended target;
5. when demoting an active administrator, count active administrators under the
   governance lock and require at least two before the change;
6. update the target role and finite `updated_at`, and increment its
   administration revision;
7. append one user-targeted `change_role` audit with exact previous/resulting
   role and administration revision; and
8. revoke every unrevoked target session at the same database time before one
   commit.

Self-role changes are forbidden. The target signs in again after every role
change, including demotion. An OIDC callback updates profile fields only and
cannot overwrite the role. Concurrent role, bootstrap, and administrator
suspension changes serialize on the same governance singleton and cannot leave
zero active administrators.

Account suspension/reinstatement remains the existing audited moderation
transaction and routes. The administration account detail links those controls
instead of adding a second implementation. AN-04 updates that transaction to
increment the same user administration revision under its existing governance/
user lock order, so a concurrent role or membership form cannot survive a
suspension-state change. Administrators can target another
role under the existing hierarchy/continuity rules; moderators retain their
existing member-only authority. No surface allows self-suspension.

### 21.5 Areas and administrator counts

The existing area core transaction remains the sole create/rename/reorder/
visibility/posting-mode mutation. AN-04 makes it revalidate the current
administrator inside the transaction, use and increment the positive numeric
administration revision, preserve the immutable slug, and append one audit row.
Create locks the actor row and any required initial group before inserting the
area and mapping. Update locks the actor row, target area, and any required
initial group in that order. The area/group mapping writer uses the same prefix
through its locked target area.
`GET /admin/areas` accepts no query or exactly the canonical
`after_order=<nonnegative int32>&after_id=<positive int64>` pair. Its
authorization-first query returns at most 26 areas ordered by
`(display_order,id)`; the handler renders 25 and row 26 is only a next sentinel.
The raw keyset does not promise a stable snapshot across concurrent reorders;
refresh starts from the beginning.

Area creation accepts exactly `_csrf`, `slug`, `name`, `description`,
`display_order`, `visibility`, `posting_mode`, `initial_group_id`, and
`reason`; it accepts no revision. Area core update takes its sole area ID from
the canonical path and accepts exactly `_csrf`, `name`, `description`,
`display_order`, `visibility`, `posting_mode`, `initial_group_id`, `reason`,
and positive numeric `revision`; it accepts neither a body target ID nor a
slug. `initial_group_id` is a canonical positive group ID exactly when creation
or a non-group-to-group transition needs the one initial mapping, and is exact
empty otherwise. Unknown, duplicate, missing, or noncanonical scalar fields
fail before the transaction.

`GET /admin/areas/{areaID}` returns one area plus at most 51 groups ordered by
group ID with a nonmultiplying assigned boolean, using optional exact
`groups_after`; the handler renders 50. Create or transition from non-group to
group visibility requires exactly one existing `initial_group_id` and creates
that mapping in the same transaction. Updating an already group-visible area
preserves its mappings; leaving group visibility removes them in the same core
transaction. A separate area/group mutation accepts exactly `_csrf`, closed
`action` (`grant` or `revoke`), `reason`, and the area's numeric revision. It
locks actor, area, and group in that order; rejects stale/no-op/overflow state,
an area not currently group-visible, and revoking the last mapping; changes one
mapping; increments the area revision; and appends exactly one
`grant_area_group` or `revoke_area_group` audit targeted to the area with the
one group ID and previous/resulting assignment boolean in bounded state.

Area core audit objects contain only slug, name, lowercase hexadecimal SHA-256
of the exact description UTF-8 bytes, display order, visibility, posting mode,
administration revision, group-mapping count, and group-mapping digest. They
never serialize the description or complete group set. The group digest input
is the concatenation of each positive sorted ID as one unsigned 64-bit big-
endian value; zero mappings use SHA-256 of empty bytes. The service streams
ordered IDs into the hash under the locked area with O(1) auxiliary memory.
Leaving group visibility deletes the mappings in one set-based statement under
that lock; its time and lock footprint are openly O(g), bounded by the
transaction timeout, and failure is atomic.

Rename changes `name`; reorder changes `display_order`; archive changes posting
mode to `archived`; restore must explicitly select `normal` or `read_only`. An
archived area remains readable to an otherwise authorized actor, while every
publication path rejects it. Area deletion is not implemented.

The dashboard query begins a read-only repeatable-read transaction, sets a
two-second statement timeout, obtains one database time, and revalidates the
administrator before aggregates. It returns nonnegative exact counts for:

- all local users and users by closed role;
- effectively active and effectively suspended users at the transaction time;
- topics with `deleted_at IS NULL`;
- posts with `deleted_at IS NULL AND redacted_at IS NULL`; and
- reports separately in `open` and `in_review`.

The three role buckets and the active/suspended buckets each reconcile to total
users. Resolved and dismissed reports are deliberately excluded rather than
folded into either moderation-work count. A
negative, NULL, unknown, internally inconsistent, timed-out, canceled, or
partially scanned result fails the whole dashboard with fixed `503`; no
approximation or stale cache is substituted. The query is openly population-
dependent and never runs for a non-administrator.

### 21.6 Routes, responses, and resources

AN-04 owns these routes in addition to the existing `/admin/areas` and account
moderation routes:

| Method | Route | Purpose |
| --- | --- | --- |
| `GET` | `/rules` | Current public community rules |
| `GET` | `/admin` | Exact administrator dashboard and navigation |
| `GET` | `/admin/accounts` | Bounded local-account list |
| `GET` | `/admin/accounts/{userID}` | Local account/role/group detail |
| `POST` | `/admin/accounts/{userID}/role` | Audited role change |
| `POST` | `/admin/accounts/{userID}/groups/{groupID}` | Audited one-membership grant/revoke |
| `GET` | `/admin/groups` | Bounded local-group management |
| `POST` | `/admin/groups` | Audited group creation |
| `POST` | `/admin/groups/{groupID}` | Audited group rename |
| `GET` | `/admin/areas/{areaID}` | Bounded area/group detail |
| `POST` | `/admin/areas/{areaID}/groups/{groupID}` | Audited one-area/group grant/revoke |
| `GET` | `/admin/settings` | Site settings form |
| `POST` | `/admin/settings` | Audited site settings update |

For every route, exact path/query grammar runs before session, body, and
database work. `/rules` performs no session lookup. Missing sessions on
protected routes enter the existing login flow; stale sessions
enter revalidation; current non-administrators receive fixed `403`. All
administrator responses are `private, no-store`, fully buffered to at most 512
KiB, and expose neither raw database errors nor submitted audit reasons.

Unsafe routes require the existing session-derived CSRF token before ordinary
form parsing, accept only `application/x-www-form-urlencoded`, reject unknown or
duplicate scalar fields, use generated request IDs, and call exactly one
service. Settings uses its documented 256-KiB body cap; area create/core update
use 64 KiB; every other AN-04-owned unsafe route uses 16 KiB. These bounds are
applied before reading/parsing and admit the worst-case URL encoding of their
bounded fields. Ordinary success is empty 303 post/redirect/get. HTMX success
is empty 204 with same-origin `HX-Location` targeting `#main-content`; both
resolve to the same builder-owned canonical destination. The sole exception is
successful `POST /admin/settings`: ordinary HTML returns the same empty 303,
while HTMX returns empty 204 with a builder-owned same-origin `HX-Redirect` and
no `HX-Location`, forcing a full document so changed shell name, description,
and theme cannot remain stale. No handler retries or detaches work after
cancellation.

The global navigation has one public `Community rules` link to `/rules` and one
`Administration` link to `/admin` shown only for the current administrator
context. Dashboard links reach areas, accounts, groups, and settings. All
controls have labels, visible focus, semantic status and error text, keyboard
access, and ordinary HTML fallbacks. Both links are base-path-built and work
independently of JavaScript.

Application logs retain route pattern, fixed outcome, status, duration, and
bounded counts only. They exclude account/group IDs and names, site/rules
content, revisions, role/group sets, audit reasons, and submitted bodies.
PostgreSQL diagnostics remain a separate operator-controlled boundary.

### 21.7 Admission and rollback

AN-04 verification uses PostgreSQL 17 with visitor/member/group/moderator/
administrator matrices, concurrent role/suspension/bootstrap and membership/
area/settings writers, migration failure/unknown-outcome inspection, strict
HTTP/HTMX behavior, site-presentation propagation across two application
instances, keyboard/no-JavaScript browser tests through Caddy, representative
population plans/resources, deterministic generation, repository integrity,
and reproducible release artifacts.

The exact final tree must retain evidence and receive two fresh independent
CLEAN reviews. After migration 000010 commits, an artifact requiring head
000009 fails exact-head readiness; rollback is forward repair/current artifact
or a verified pre-000010 database restore. No application fallback drops audit,
rules-renderer, or authorization checks to run an older binary.

## 22. AN-05 basic abuse controls

### 22.1 Immutable configuration

`config.Load` requires and validates the seven AN-05 rate settings and the
`ABUSE_RULES_FILE` path. Before opening PostgreSQL or a listener,
`abuse.LoadPolicy` descriptor-opens and validates the file and returns one
`AbusePolicy` containing plain rate values and an immutable blocked-destination
matcher. It exposes no raw rules or mutable map. Startup accepts an exact empty
rules file as no blocked destinations but rejects a missing file. The request
limiter reads its 256-bit digest key from `crypto/rand` in that same pre-I/O
construction phase; entropy failure aborts startup.

The rules file is UTF-8 with LF line endings, no BOM, CR, NUL, controls, blank
lines, or comments. It contains at most 256 nonempty lines and is strictly
sorted by canonical byte value with no duplicate. Each line is one of:

```text
domain=example.org
url=https://example.org/exact/path?key=value
```

Domain input rejects a trailing dot and IP literal, is converted through the
`golang.org/x/net/idna` lookup profile, lowercased, and then required to satisfy
DNS label/length rules. URLs require lowercase `http` or `https`, no userinfo,
no opaque form, and either a canonical `netip` IPv4/IPv6 literal or an IDNA DNS
host without a trailing dot. IPv6 retains brackets. Default `:80`/`:443` is
removed and empty path becomes `/`. Path and raw query uppercase every retained
percent escape and decode only RFC 3986 unreserved bytes. The path then removes
literal `.`/`..` segments without decoding escaped separators; query ordering
and reserved delimiters remain significant. Fragment is discarded. Rules with
a nondefault numeric port remain port-specific.
Any input that does not already equal the emitted canonical rule fails startup;
the loader does not silently repair operator input.

### 22.2 Client identity and request limiter

Production Caddy uses:

```caddyfile
reverse_proxy 127.0.0.1:18082 {
    header_up X-Forwarded-For {remote_host}
    header_up -Forwarded
    header_up -X-Real-IP
}
```

The service parses `RemoteAddr` with `net.SplitHostPort` and `netip.ParseAddr`,
rejecting zones and unmapping IPv4-in-IPv6. For a loopback peer, every charged
production request requires exactly one canonical `X-Forwarded-For` address
with no comma, whitespace, port, or zone. For a non-loopback peer in test or
development, the forwarded header must be absent and the peer address is the
client. Malformed or contradictory identity returns fixed `400` before any
downstream call. Loopback health checks and exact content-addressed static
`GET`/`HEAD` routes are exempt and do not require the header.

`RequestLimiter` is constructed with one unpredictable 256-bit process key,
one mutex, one pre-sized map, the configured capacity, count, window, and
clock. The map key is HMAC-SHA-256 under that key over the fixed ASCII domain
`gotth-bb/request-client/v1`, a zero byte, and the canonical 4- or 16-byte
address; no raw address or string is retained. A collision conservatively
shares one window rather than allocating a disambiguation copy of the address;
tests inject that otherwise infeasible condition. Each entry stores only window
start and count. The limiter also maintains a conservative earliest-expiry
timestamp. On capacity pressure while `now` is earlier than that timestamp, an
unseen digest receives capacity rejection in O(1). At or after that timestamp,
one O(capacity) pass removes every expired entry and recomputes the minimum
live expiry; it cannot repeat before that new boundary unless an injected clock
moves backward, which still returns O(1) rejection. A charged known client is
admitted/incremented or receives rate rejection with the remaining whole-
second ceiling clamped to `[1, windowSeconds]`. The limiter never exceeds the
configured entry count and starts no goroutine or timer.

The request-ID boundary remains outermost. Access logging and panic recovery
wrap the limiter, so every rejection has one ordinary completion record and a
request ID. Before writing an early rejection it sets only the fixed
`request-admission` pattern, preventing an empty or raw attacker-controlled
route label. The limiter runs before session middleware and the application
router. Its exemption classifier is a closed method/path matcher shared with
the static/health dispatch contract; unknown paths are charged. `429` and `503`
set `Cache-Control: no-store` and `Retry-After` and write a fixed small body.
The middleware does not read or close the request body.
It applies one client-address profile regardless of eventual authentication;
no post-authentication request limiter or account-age lookup is added.

### 22.3 Migration 000011 and durable publication windows

Migration 000011 is a stopped ordinary atomic migration. It adds to `users`:

```sql
publication_window_started_at timestamptz,
publication_count integer NOT NULL DEFAULT 0
```

The migration adds and validates a finite `users.created_at` check because age
now selects security policy. The publication check requires either `(NULL, 0)`
or a finite non-NULL start no earlier than account creation with a count from 1
through 100,000. Existing accounts therefore receive the
constant-default empty tuple without rewrite. Migration validation scans the
account relation and takes real locks; application and every writer remain
stopped. Runtime receives column-level UPDATE on only these two new columns,
not table-wide UPDATE. Readiness attests the columns, defaults, nullability,
check, and exact grant delta at migration head 000011.

Each topic/reply transaction uses read committed isolation and installs
`statement_timeout = '2s'` and `lock_timeout = '250ms'` with transaction-local
scope before its first application lock. It then locks the
current user row and returns only id, role, suspension/mute facts, `created_at`,
and the publication tuple. It rejects an invalid, changed, suspended, or muted
actor, then reloads the current local group IDs while holding the user lock.
Membership writers lock that same row before changing a mapping, so the
transaction authorizes target policy with one database-current actor rather
than the request snapshot. After target authorization and immediately before
counter mutation, a separate statement reads `clock_timestamp()`. The service
rejects database time before account creation, computes account age by checked
subtraction, and chooses the strict limit while age is less than
`NEW_ACCOUNT_PERIOD`; equality is established. It likewise determines window
age by subtraction rather than by adding a duration to a stored PostgreSQL
timestamp. A start ahead of database time by at most one configured window is
treated as an active window after a bounded clock regression, with
`Retry-After` clamped to one window. A start farther in the future is malformed
stored state and makes publication unavailable. An elapsed or empty window
becomes `(database_now, 1)`; an active below-limit window increments; an active
at-limit window returns the ceiling number of seconds to its end. No decision
can overflow by adding a duration to a maximum finite PostgreSQL timestamp.

Content input and blocked-destination validation happen before transaction
begin. Inside the transaction, account revalidation precedes area/topic target
locks; target authorization precedes the counter update; the counter update
precedes the topic/reply insert. All changes commit or roll back together.
`ErrPublicationRateLimited` carries only a positive bounded retry duration.
Commit ambiguity uses the existing unknown-outcome behavior. No retry, event
row, cleanup task, advisory lock, or process cache exists.

### 22.4 Blocked-destination matcher

The renderer's one internal parse helper first performs existing source
validation, builds the admitted GFM AST, and visits every resolved link, image,
and automatic-link destination before rendering that same tree. External
destinations must parse as absolute HTTP(S) URLs. Their canonical comparison
key discards userinfo, so an authored credential variant cannot evade a domain
or exact-URL rule; the existing renderer/sanitizer remains responsible for
whether that authored destination is presented. One trailing DNS root dot is
discarded for comparison; rule input itself must omit it. Path/query percent
normalization and path dot-segment removal use the exact rule canonicalizer.
The canonical host is compared
with exact and dot-boundary domain rules; the canonical URL is compared with
exact URL rules. Local relative references, anchors, and `mailto` destinations
remain governed by the existing renderer/sanitizer and do not match external
rules. A network-path reference beginning `//` and any parsed destination
containing an ASCII backslash are rejected as normal field-safe Markdown
validation failures because browser HTTP(S) URL resolution treats paired or
mixed leading slashes/backslashes as an external authority. A malformed HTTP(S)-
looking destination is likewise a normal Markdown validation failure, not a
bypass.

`RenderTopicDraft`, `RenderReplyDraft`, `CreateTopic`, `CreateReply`,
`EditPost`, and the nonempty community-rules rendering path receive the
immutable policy explicitly. Every call site, including tests, supplies either
the validated runtime policy or a deliberate validated empty policy; there is
no optional argument, package global, or compatibility wrapper that production
can accidentally use. Preview and mutation use the same service function. A
blocked result is typed separately for the fixed observer class but unwraps to
field-safe Markdown validation; it retains no source, destination, or rule.

Publication rate applies only to committed new topic/reply rows. Edit and all
preview routes enforce the destination policy but do not call the publication
counter. The administrator settings update enforces it for nonempty community-
rules Markdown before beginning its existing audited transaction and likewise
spends no publication capacity. Delete, other moderation/administration, OIDC,
and read routes do neither. Existing post and rules rows are not backfilled or
rescanned.

### 22.5 HTTP and observability

Rate-limited topic/reply submission renders the ordinary bounded form with the
escaped submitted draft and fixed “Please wait before publishing again” text,
status `429`, `Retry-After`, and `private, no-store`. HTMX receives the same
main-region presentation and status; neither path redirects. A blocked
destination uses the existing Markdown field presentation with fixed “This
draft contains a blocked link” text and `422`. Preview and settings use the same
result and settings preserve the escaped source plus current revision.
No response distinguishes domain from exact-URL match.

One injected `AbuseObserver` emits only the fixed class, the fixed
`request-admission` label or matched route pattern,
request ID, and bounded retry seconds. It is called once after terminal
selection for a limiter, publication-rate, or blocked-destination rejection.
The existing access logger still records method, route, status, bytes, and
duration. Neither observer logs client identity, account identity/age, raw
request target, query, form, Markdown, destination, rule, configured count, or
map occupancy. Logging failure cannot change the response or database result.

### 22.6 Admission and rollback

AN-05 verification covers strict configuration/file/descriptor handling,
client-header spoofing and malformed identity, bounded map saturation and
restart reset, body non-consumption, fixed-window boundaries, concurrent
publication serialization, new-account transition, rollback/unknown commit,
blocked link/image/autolink/reference cases, IDNA/subdomain/port/path/query
normalization, preview/mutation parity, role and authorization matrices,
redacted logs, full-page/HTMX/no-JavaScript behavior, PostgreSQL 17 migration/
grant/readiness evidence, resource bounds, Caddy/Chromium behavior,
deterministic generation, repository integrity, and reproducible artifacts.

The exact final tree retains evidence and receives two fresh independent CLEAN
reviews. After 000011 commits, an artifact requiring head 000010 fails exact-
head readiness. Rollback is forward repair/current artifact or a verified pre-
000011 database restore. Running an older binary is forbidden because it would
publish without the admitted durable counter and would fail readiness.

## 23. Beta.1 admission and recovery

Beta.1 changes no runtime feature boundary unless the admitted inventories find
a concrete version 1.0 defect. Its implementation consists of deterministic
inventory/gate code, scoped defect corrections, two direct logical-recovery
scripts packaged with the release, and exact release/deployment evidence.

### 23.1 Requirement and route inventories

One repository test parses the PRD's exact bold requirement-ID grammar and the
canonical Beta traceability data. It rejects omitted, duplicate, unknown, or
malformed IDs and empty implementation/evidence references. The traceability
data is test/evidence input; production does not parse it and it cannot grant
authority or register routes.

One HTTP-package test uses Go's parser and syntax tree to extract literal
method/pattern registrations from the exact production router construction
sources. Middleware-owned liveness, readiness, and content-addressed static
routes are appended explicitly. The canonical reviewed inventory records
method, normalized path pattern, actor/session class, CSRF use, response mode,
cache class, database/mutation/audit effect, and denial class. Tests compare the
extracted method/pattern set to that inventory in both directions. A separate
template and redirect-source scan compares emitted static route/action shapes
to it. Dynamic identifiers normalize to their registered brace pattern;
test-only sources are excluded explicitly. Production router construction is
not refactored into a metadata framework merely to make the test convenient;
request dispatch remains the existing standard-library router.

### 23.2 Leakage, accessibility, and representative gates

The Beta integration harness composes existing service boundaries with
deterministic identities, policy fixtures, clocks, and fault injectors. It does
not add an application backdoor. Every route record selects its applicable
actor/state/leakage cases; an empty selection for a database-backed or
authorization-sensitive route is a test failure. Full-page and HTMX requests
share the same route and service path. Browser checks run only through Caddy
and record adapted configuration before accepting forwarded client identity.

Automated accessibility checks use the rendered accessibility tree and a pinned
local checker when available; manual keyboard, focus, zoom, reflow, contrast,
and status evidence remains required because automated success is not a human
usability claim. The harness stores no credentials or restricted page bodies in
Git. Screenshots and trees are retained only when they contain disposable
fixtures and materially prove a named row.

Representative-plan checks reuse the existing deterministic population and
plan parsers. Beta adds one table of core routes and expected maximum query
counts/row bounds; it does not introduce a generic benchmark framework. Both
custom and generic plans are structurally inspected. Every resource test
captures a pre-wave baseline and requires pool/goroutine/file-descriptor state
to converge back to that baseline within a bounded interval.

### 23.3 Logical backup helper

The release adds `deploy/postgresql/backup-logical.sh`. It accepts exactly an
existing PostgreSQL container name and an absolute output archive path. The
container name uses Docker's conservative ASCII name grammar; the output parent
must already exist, be an absolute real directory, and the final archive and
`.sha256` sidecar must not exist. The helper sets `umask 077`, creates its
temporary regular file in the output directory, and rejects symlink/non-regular
targets.

It runs the pinned PostgreSQL 17 container's own `pg_dump` as the container's
`postgres` operating-system user. Inside the container, `POSTGRES_USER` and
`POSTGRES_DB` select the local migration-owner connection; no database URL or
password becomes a host process argument. The exact dump is custom format,
`--no-privileges`, `--serializable-deferrable`, and a five-second lock-wait
timeout. Output streams once through Docker into the temporary host file. After
successful close, the helper streams the temporary archive on standard input to
the same pinned container's `pg_restore --list`, syncs the file, computes
SHA-256, atomically
renames the archive, then atomically installs a fixed-format sidecar. A final
archive without a valid matching sidecar is incomplete and cannot be restored
or released. Failure removes only helper-owned temporary files; it never
overwrites an existing backup.

The helper emits one bounded success line with basename, byte count, and digest.
Errors name the failed stage without connection data, environment values, dump
contents, or broad command output. Cancellation and signal handling stop the
pipeline and leave no admitted final pair.

### 23.4 Logical restore helper

The release adds `deploy/postgresql/restore-logical.sh`. It accepts exactly a
clean task-owned PostgreSQL 17 container name and an absolute archive path. The
archive must be a non-symlink regular file with a matching fixed-format
`.sha256` sidecar. The helper verifies the digest and validates the archive with
the target container's pinned `pg_restore --list` before database work.

The target container must report PostgreSQL major 17, must expose nonempty
`POSTGRES_USER` and `POSTGRES_DB`, and its target database must contain no
non-extension user relations and no migration ledger. The helper then streams
the archive once to `pg_restore --exit-on-error --single-transaction
--no-privileges` as the container's `postgres` operating-system user and
migration-owner database role. A restore failure leaves the task-owned target
for inspection; it is not silently dropped or retried. A second run rejects the
now-nonempty database.

Runtime privileges are deliberately absent from the archive and restore. The
operator must apply the exact packaged `runtime-grants.sql` after creating the
target runtime role, then run migration-head/readiness and application smoke
checks. The helper emits only archive basename, digest, target container name,
and a fixed committed result; no row data or credential is output.

Both helpers have shell syntax/static checks plus fake-Docker tests for argument
grammar, existing/symlink targets, short/failed output, archive/list/digest
failure, atomic admission, clean-target refusal, restore failure, cancellation,
redaction, and exact command/stdin ordering. A real PostgreSQL 17 integration
creates a source database with every migration and representative private/audit
state, backs it up, restores it into a distinct clean container/database,
reapplies packaged grants, and proves row/schema identity, readiness, and smoke.

### 23.5 Alpha.2 upgrade and release state machine

The rehearsal begins from a logical copy of the actual Alpha.2 database at
migration 000005. The stopped migration runner applies 000006 through 000011;
the renderer and search completion commands run at their required boundaries,
and packaged grants are reapplied after all schema changes. Fixtures prove
stable identities, content trees, reports/audits, visibility, and counts survive
the full chain. Failure injection covers each migration/completion boundary and
unknown outcomes; no test mutates the live database.

Live release follows the state machine in `release-operations.md`. The active
Docker application container is the only application service stopped/replaced.
The disabled Alpha.1 systemd unit is never started as a fallback. PostgreSQL,
its bind mount, Caddy, root-owned configuration/secrets, the previous image and
Compose environment, and the verified pre-upgrade backup are preserved.

The release artifact includes both logical-recovery helpers with mode `0755`,
the runtime-grant artifact, container files, dependency manifest, and exact
release identity. Package construction loads each file from the specified
commit, validates bounded text bytes and modes, and includes them in normalized
archive ordering. Two independent builds must be byte-identical.

After migration 000011, Alpha.2 is not an executable rollback against the live
schema. Until owner confirmation, rollback is current Beta/forward repair or a
verified pre-upgrade restore followed by the preserved Alpha.2 artifact. Only
owner confirmation records the Beta commit/artifact as known-good.

## 24. Definition of implementation complete

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
