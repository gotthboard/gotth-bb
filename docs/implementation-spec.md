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
- `net/http` with a small Go router such as Chi; route semantics shall be
  verified against its current documented contract before selection.
- Templ for compiled server-side components.
- HTMX pinned and served as a versioned static asset.
- Tailwind CSS pinned and run at build time.
- `pgx/v5` for PostgreSQL access.
- `sqlc` for typed query generation from reviewed SQL.
- A migration tool that records ordered migrations in PostgreSQL and fails on
  drift; the exact tool is selected after its up/down and transaction behavior
  is verified.
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

| Setting | Required | Purpose |
| --- | --- | --- |
| `APP_ENV` | Yes | `development`, `test`, or `production` |
| `LISTEN_ADDR` | Yes | Internal bind address, normally loopback |
| `PUBLIC_BASE_URL` | Yes | Exact external base, including `/bb` |
| `BASE_PATH` | Yes | Browser path prefix, `/bb` in production |
| `DATABASE_URL` | Yes | PostgreSQL connection string supplied as a secret |
| `OIDC_ISSUER_URL` | Yes | Exact Authentik issuer |
| `OIDC_CLIENT_ID` | Yes | OIDC client identifier |
| `OIDC_CLIENT_SECRET` | Yes in production | Confidential-client secret |
| `SESSION_COOKIE_NAME` | No | Defaults to a host-specific opaque name |
| `SESSION_MAX_AGE` | Yes | Absolute authenticated-session lifetime |
| `SESSION_IDLE_TIMEOUT` | Yes | Idle session expiry |
| `AUTH_REVALIDATE_INTERVAL` | Yes | Maximum accepted Authentik identity staleness |
| `LOG_LEVEL` | No | Structured log severity threshold |

Rules:

- `PUBLIC_BASE_URL` must be HTTPS in production, contain no query or fragment,
  and its path must equal normalized `BASE_PATH`.
- `BASE_PATH` is empty or begins with one `/`, has no trailing slash, and
  contains no traversal or encoded separator.
- OIDC callback is computed as `PUBLIC_BASE_URL + /auth/callback`; it is not a
  separate free-form setting.
- OIDC claims never assign forum roles or local group membership.
- Secrets are not available to templates, logs, diagnostics, or health output.

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

### 5.3 Explicit policy functions

The initial policy API remains small:

- `CanViewArea(actor, areaPolicy) bool`
- `CanCreateTopic(actor, areaPolicy) bool`
- `CanReply(actor, areaPolicy, topicState) bool`
- `CanEditPost(actor, postOwnership, postState) bool`
- `CanDeletePost(actor, postOwnership, postState) bool`
- `CanModerate(actor) bool`
- `CanAdminister(actor) bool`

Read queries still enforce access in SQL. These functions govern mutation and
provide unit-testable explanations. Do not create a stringly typed
`Can(actor, action, resource)` framework for seven operations.

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

### 6.2 `external_identities`

- `user_id` foreign key and unique for version 1.0 single-issuer operation
- `issuer`
- `subject`
- `created_at`, `last_verified_at`
- unique `(issuer, subject)`

Issuer and subject are not user-editable.

### 6.3 `forum_groups` and `forum_group_members`

`forum_groups` contains a stable generated ID, unique bounded name, creator,
and timestamps. `forum_group_members` contains `group_id`, `user_id`, the
administrator that granted membership, and timestamps, with primary key
`(group_id, user_id)` and an index on `(user_id, group_id)`. Role and membership
changes append audit events in the same transaction.

### 6.4 `sessions`

- `id`
- `token_hash` unique
- `user_id`
- `issued_at`, `last_seen_at`, `validated_at`, `expires_at`
- `revoked_at` nullable
- bounded `user_agent_hash` and `ip_prefix` or equivalent audit fields

Only a cryptographic hash of the opaque token is stored. Session lookup uses a
constant-time comparison where application comparison is required. Expired and
revoked sessions never authenticate.

### 6.5 `oidc_login_attempts`

- hashed/state lookup key
- protected nonce and PKCE verifier material
- purpose (`login` or `revalidate`) and existing session reference when needed
- `return_path`, validated as an internal `/bb` path
- `created_at`, `expires_at`, `consumed_at`

State is single use. Cleanup is bounded and safe to repeat.

### 6.6 `areas` and `area_groups`

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

### 6.7 `topics`

- `id`, `area_id`, `author_id`, `title`, normalized optional slug
- `state` such as `open`, `locked`, `hidden`, `archived`
- `pinned_at` nullable
- `first_post_id`, `latest_post_id` established within creation transaction
- `reply_count`, `next_post_number`
- `created_at`, `updated_at`, `last_activity_at`, `deleted_at` nullable

Indexes support area lists ordered by pinned/activity and recent activity
ordered globally after access filtering.

### 6.8 `posts`

- `id`, `topic_id`, `author_id`, `post_number`
- `markdown_source`
- `rendered_html`, `renderer_version`
- `revision`
- `created_at`, `updated_at`, `edited_at`, `deleted_at`
- `deleted_by`, `deletion_reason` nullable
- unique `(topic_id, post_number)`

Source and rendered sizes have limits. A post edit increments `revision` and
uses `WHERE revision = $expected` to detect lost updates.

### 6.9 `topic_reads`

- `user_id`, `topic_id`, `last_read_post_number`, `read_at`
- primary key `(user_id, topic_id)`

Updates use `GREATEST` so an out-of-order request does not mark a topic less
read.

### 6.10 Reports and audit

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

## 8. OIDC implementation

### 8.1 Discovery and validation

At startup, load discovery only from `OIDC_ISSUER_URL`. Validate that returned
issuer matches exactly. Do not accept request-provided issuers. Cache keys
according to the chosen library's documented behavior and expose discovery
failure through readiness only when it prevents safe operation.

### 8.2 Login attempt

- Generate at least 256 bits of random state and nonce material.
- Generate a PKCE verifier using a cryptographic random source and S256
  challenge.
- Expire attempts quickly and limit outstanding attempts per browser/session.
- Validate return paths against the configured base path; never redirect to an
  arbitrary absolute URL.
- Record whether the attempt is initial login or session revalidation.

### 8.3 Callback transaction

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

For revalidation, the callback additionally verifies the existing session,
updates the identity/profile snapshot, creates a rotated replacement session,
and revokes the old session in the transaction. A failed or abandoned
reauthorization leaves the stale session unable to authorize protected routes.

The first administrator is granted only through an explicit operator command
against an already provisioned `(issuer, subject)` identity. The command
requires database/operator authority, rejects a missing or ambiguous identity,
and commits the local role change with an immutable `actor_kind=operator` audit
event. OIDC claims do not bootstrap or restore local privileges, and the
command never invents a forum-user actor for the first grant.

## 9. Session implementation

- Cookie value contains only a random opaque token.
- Production cookie flags: `Secure`, `HttpOnly`, `SameSite=Lax`, `Path=/bb`.
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
  per request.

## 10. HTTP and route contract

Internal routes are shown without the external `/bb` prefix.

| Method | Route | Purpose | Minimum actor |
| --- | --- | --- | --- |
| `GET` | `/` | Area index | Visitor |
| `GET` | `/login` | Start Authentik login | Visitor |
| `GET` | `/auth/callback` | OIDC callback | Login attempt |
| `POST` | `/logout` | Revoke local session | Member |
| `GET` | `/areas/{slug}` | Area topic list | Area viewer |
| `GET` | `/topics/{id}` | Topic and posts | Area viewer |
| `GET` | `/topics/new` | New-topic form | Eligible member |
| `POST` | `/topics` | Create topic | Eligible member |
| `POST` | `/topics/{id}/replies` | Create reply | Eligible member |
| `GET` | `/posts/{id}/edit` | Edit form | Author/staff |
| `POST` | `/posts/{id}/edit` | Apply edit | Author/staff |
| `POST` | `/posts/{id}/delete` | Soft delete | Author/staff |
| `POST` | `/reports` | Create report | Member |
| `GET` | `/moderation/reports` | Moderation queue | Moderator |
| `POST` | `/moderation/actions` | Moderation transition | Moderator |
| `GET` | `/admin/areas` | Area management | Administrator |
| `POST` | `/admin/areas` | Create area | Administrator |
| `POST` | `/admin/areas/{id}` | Change area | Administrator |
| `GET` | `/search` | Access-filtered search | Visitor |
| `GET` | `/health/live` | Liveness | Edge/operator |
| `GET` | `/health/ready` | Readiness | Edge/operator |

Use POST for browser mutations. Method override tricks are not required in
version 1.0. Route bodies, path IDs, query lengths, and pagination sizes are
bounded.

## 11. Full-page and HTMX responses

- `HX-Request` selects a documented fragment only after the same handler,
  authorization, validation, and service path runs.
- Full-page successful form submission uses a `303` redirect.
- HTMX success may return a fragment plus `HX-Redirect` or documented swap
  headers.
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

- Canonical input is bounded UTF-8 Markdown source.
- Raw HTML input is disabled initially.
- Link schemes are restricted to an allowlist.
- Rendered HTML passes through a narrow sanitizer allowlist even when the parser
  claims safe output.
- External links use the documented rel policy.
- Rendered output carries a renderer version for deterministic rebuilding.
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

## 15. Migrations

- Migration filenames are ordered and immutable after a release consumes them.
- Each migration documents lock and data-rewrite risk.
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

Public errors expose a request ID and useful next action without internal SQL,
filesystem, issuer, or stack details. Internal errors retain wrapped causes for
operator logs.

## 17. Health and shutdown

- Liveness reports whether the process event loop can serve requests.
- Readiness checks required configuration, migration compatibility, and a
  bounded PostgreSQL round trip.
- Authentik availability does not necessarily make existing-session reads
  unready; login failures are reported separately.
- Shutdown stops accepting new requests, drains bounded in-flight work, closes
  the database pool, and exits nonzero when shutdown fails.

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
- Correct `/bb` links, cookies, redirects, assets, and callback.
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
