# Traceability and verification plan

## Document control

| Field | Value |
| --- | --- |
| Status | Draft constrained by product and implementation contracts |
| Product contract | [Product requirements](prd.md) |
| Delivery contract | [Feature plan](feature-plan.md) |

## 1. Verification rule

A passing build is not sufficient evidence. Verification must show that the
required behavior works, forbidden behavior fails closed, restricted data does
not leak through alternate paths, and operators can deploy and recover the
system.

Evidence is attached to the requirement or release gate it proves. Logs saying
"tests passed" without the command, revision, environment, and result are not
evidence.

## 2. Evidence classes

| Code | Evidence |
| --- | --- |
| `UT` | Deterministic unit test |
| `DB` | PostgreSQL integration or migration test |
| `HTTP` | HTTP/HTMX integration test |
| `E2E` | Browser or deployed-environment journey |
| `SEC` | Security-specific automated or manual test |
| `A11Y` | Accessibility automation and manual keyboard review |
| `OPS` | Deployment, backup, restore, rollback, or failure rehearsal |
| `REV` | Human design/code/evidence review |

Canonical evidence belongs in CI results, release records, or a reviewed
`docs/evidence/` artifact. Worker scratch is not canonical merely because it
exists.

## 3. Requirement traceability

| Requirements | Primary implementation surface | Required evidence | First gate |
| --- | --- | --- | --- |
| ID-001–ID-005 | `auth`, identity store, local role/group stores | UT, DB, HTTP, E2E, SEC | Alpha.1 |
| ID-006–ID-009 | session store/middleware, suspension policy | UT, DB, HTTP, SEC | Alpha.1 |
| ID-014–ID-016 | registration modes, pending identity, restricted Authentik reconciliation | UT, DB, HTTP, E2E, SEC, OPS | B1-09 |
| ACL-001–ACL-003 | area schema and policy types | UT, DB, REV | Alpha.1 |
| ACL-004–ACL-006 | every area-owned query and route | DB, HTTP, E2E, SEC | Alpha.1 |
| ACL-007–ACL-008 | admin/moderation transactions and audit | DB, HTTP, REV | Alpha.1 |
| FORUM-001–FORUM-002 | area schema, stores, and pages | UT, DB, HTTP, E2E | Alpha.1 |
| FORUM-003–FORUM-004 | parent-addressed publishing and threaded topic pages | UT, DB, HTTP, E2E | Alpha.2 |
| FORUM-005–FORUM-006 | moderation state and stable URLs | UT, DB, HTTP, E2E | Alpha.1 |
| CONTENT-001–CONTENT-007 | composer, renderer, post services | UT, DB, HTTP, SEC | Alpha.1 |
| READ-001 | index/list/threaded-topic handlers | HTTP, E2E, A11Y | Alpha.1 baseline; Alpha.2 threaded |
| READ-002 | read markers and unread views | UT, DB, HTTP, E2E, SEC, PERF | Alpha.N (AN-03) |
| READ-003–READ-004 | PostgreSQL search/activity queries | DB, HTTP, SEC, E2E, PERF | Alpha.N (AN-02) |
| READ-005 | URL builder and templates | UT, HTTP, E2E | Alpha.1 |
| READ-006 | tree-order paging and reply-to context | UT, DB, HTTP, E2E, A11Y | Alpha.2 |
| MOD-001–MOD-002 | report service and queue | UT, DB, HTTP, E2E | Beta.1 |
| MOD-003–MOD-004 | moderation transitions | UT, DB, HTTP, E2E | Alpha.1 minimum; Beta complete |
| MOD-005–MOD-006 | abuse controls | UT, HTTP, SEC, OPS | Beta.1 |
| MOD-007–MOD-008 | audit transaction/store | DB, HTTP, REV | Alpha.1 |
| ADMIN-001–ADMIN-003 | area/account/group administration | UT, DB, HTTP, E2E | Alpha.1 minimum; Beta complete |
| ADMIN-004–ADMIN-005 | settings and authorized counts | DB, HTTP, E2E | Beta.1 |
| ADMIN-006–ADMIN-009 | Board control settings, registrations, sessions, and email operations | UT, DB, HTTP, E2E, A11Y, SEC, OPS | B1-09 |
| UX-001–UX-005 | Templ/HTMX/Tailwind UI | HTTP, E2E, A11Y | Beta.1 |
| SEC-001–SEC-005 | middleware, rendering, logging, config | UT, HTTP, SEC, REV | Alpha.1, repeated at RC |
| OPS-001–OPS-005 | migrations, health, logs, deployment | DB, OPS, REV | Alpha.1 minimum; Stable complete |
| OPS-006 | standalone six-service deployment and dual-database recovery | E2E, SEC, OPS, REV | B1-08 |

Every implementation issue narrows these grouped rows to the exact requirement
IDs it changes.

## 4. Access matrix

Legend: `V` view, `T` create topic, `R` reply, `S` staff publish, `-` denied.
Suspension removes `T` and `R` from every non-staff row. Archived mode removes
all publishing until restored.

| Actor | Public normal | Public read-only | Auth normal | Auth read-only | Group normal, match | Group normal, no match |
| --- | --- | --- | --- | --- | --- | --- |
| Visitor | V | V | - | - | - | - |
| Member | VTR | V | VTR | V | - unless matched | - |
| Matching-group member | VTR | V | VTR | V | VTR | - for other groups |
| Moderator | VTRS | VTRS | VTRS | VTRS | VTRS | VTRS |
| Administrator | VTRS | VTRS | VTRS | VTRS | VTRS | VTRS |

Additional states tested for each relevant actor:

- Topic open versus locked.
- Area normal, read-only, and archived.
- User active, muted, suspended, and suspension expired.
- Post author versus other member.
- Existing object versus unauthorized object versus nonexistent object.
- Current forum-local group membership versus audited removal, which takes
  effect on the next protected request.
- Current Authentik identity versus disable after the configured revalidation
  boundary.

## 5. Leakage test inventory

For a fixture topic inside a group-restricted area, every unauthorized actor is
tested against:

- Area index.
- Area topic list.
- Recent activity.
- New/unread counts.
- First-unread redirect.
- Search result, count, rank, and snippet.
- Direct area, topic, and post URL.
- Canonical-link and metadata generation.
- Breadcrumbs and navigation.
- Author profile activity.
- Report target lookup.
- Moderation and administration summaries.
- HTMX fragments corresponding to every full-page route.
- Any cache introduced later.
- RSS, related-topic, notification, API, or federation surfaces when those
  versions add them.

The expected result is absence without existence disclosure. A `403` in one
route and `404` in another can itself become a disclosure, so behavior is
specified and tested consistently.

## 6. Identity and session tests

Automated tests cover:

- Correct issuer, audience, signature, expiry, nonce, state, and PKCE.
- Wrong issuer, wrong audience, invalid signature, expired token, missing
  subject, malformed/oversized approved profile claims, and callback replay.
- Identity collision and mutable-email change.
- OIDC claims cannot grant moderator, administrator, or local-group access.
- Audited local member/moderator/administrator role transitions.
- Audited local group grant and removal with immediate access change.
- First-administrator operator grant rejects missing and ambiguous identities.
- First-administrator grant rejects later/concurrent attempts once one active
  administrator exists.
- Demotion or suspension rejects transitions that would remove the last active
  administrator, including concurrent attempts.
- Governance singleton locking is exercised with concurrent bootstrap,
  demotion, and suspension transactions; the completeness oracle is at least
  one unsuspended administrator-role row after every committed transition.
- Fresh migration proves exact `governance_state` cardinality one. A missing
  row makes readiness fail. A PostgreSQL 17.10 restricted-role integration
  proves the lock fails before the deployment grant, succeeds after the grant,
  and the runtime role still lacks table-wide UPDATE, UPDATE on `created_at`,
  and DELETE.
- Successful session rotation and old-token rejection.
- Idle and absolute expiry.
- Revoked and locally suspended sessions.
- Local logout success and stale-CSRF recovery, including proof that a stale
  request neither revokes the session nor expires its cookie.
- Logout when Authentik logout succeeds, fails, or is unavailable.
- Authentik disable at the documented revalidation boundary.
- Token, code, cookie, verifier, and secret redaction in logs.

Deployed E2E verification uses a dedicated Authentik application and test
identities. Forum roles and groups are assigned locally for the access matrix;
production identities are not fixtures.

## 7. Content and concurrency tests

- Markdown XSS corpus and unsafe URL schemes.
- Unicode, empty/whitespace, maximum-size, and over-limit content.
- Preview/publish equivalence for the same renderer version.
- Stale edit revision conflict.
- Concurrent replies allocate unique consecutive post numbers.
- Concurrent replies to different parents allocate unique consecutive post
  numbers and immutable parent/path pairs without lost topic activity.
- Parent validation rejects nonexistent, deleted, cross-topic, later,
  self-referential, cyclic, and over-depth relationships without existence
  disclosure.
- Tree-order paging is deterministic; stable post links calculate the same
  page; a child whose parent is outside the page retains safe reply-to context.
- Soft-deleted leaves disappear, while deleted ancestors with visible
  descendants render body-free tombstones and never expose retained content.
- Locked topic and read-only/archived area races.
- Author deletion, moderator restoration, and redaction semantics.
- Transaction rollback after simulated audit failure.
- Duplicate form submission and retry after connection loss.

## 8. Database and migration tests

Each migration set is verified by:

1. Migrating an empty supported PostgreSQL instance to head.
2. Loading deterministic fixtures.
3. Running schema constraints and repository tests.
4. Migrating a snapshot from the previous release to head.
5. Running application smoke tests against the upgraded database.
6. Exercising the documented rollback or restore path.

The alpha.2 thread migration additionally proves the alpha.1 flat fixture
backfills every reply beneath the correct first post, invalid parent/path state
cannot commit, the tree-order index is usable, and the previous alpha.1 binary
can still publish a direct-root reply through the documented compatibility
boundary.

Tests inspect foreign keys, unique constraints, check constraints, indexes used
by access-controlled lists, and migration-table consistency. A down migration
that destroys data is not accepted as a rollback story.

## 9. HTTP and HTMX parity

Every HTMX mutation is paired with a normal-form test proving the same:

- Actor and permission decision.
- CSRF decision.
- Validation rules.
- Transaction and audit behavior.
- Success destination.
- Failure status and visible error.

Fragment rendering must not become a second handler with weaker controls.

## 10. Accessibility verification

Automated checks run on core pages, followed by manual review of:

- Keyboard-only login, navigation, topic creation, reply, edit, report, and
  moderation.
- Logical heading and landmark structure.
- Visible focus and no keyboard trap.
- Form labels, descriptions, errors, and status announcements.
- HTMX focus placement and error announcements after swaps.
- Color contrast and non-color status indicators.
- Mobile reflow and zoom without lost controls.

Stable 1.0 cannot ship with a known blocker in a core keyboard flow.

## 11. Security verification

Before beta and repeated before stable:

- Dependency and known-vulnerability scan.
- Secret scan of repository and built artifact.
- Manual route/method inventory against authorization requirements.
- CSRF tests for every mutation.
- Cookie and security-header inspection through Caddy.
- OIDC redirect and return-path manipulation tests.
- SQL injection and parameter-boundary tests.
- Stored/reflected XSS tests.
- Rate-limit bypass and proxy-address trust tests.
- Log review for restricted content and credentials.
- Restricted-content leakage suite.

Threats and accepted residual risks are recorded with an owner and target
release. "Low probability" is not a substitute for a boundary.

## 12. Performance and capacity verification

Alpha records a baseline rather than inventing a scale claim. The representative
fixture includes multiple public, authenticated, and group-restricted areas;
active and archived topics; and enough posts to exercise pagination and search.

Measure:

- Request latency and allocation profile for index, area, topic, and search.
- SQL query count and query plan for each core page.
- Connection-pool utilization.
- Concurrent reply throughput and lock contention.
- Session write frequency.
- Migration and backup/restore duration.

Before stable, owner-approved capacity expectations turn these baselines into
release budgets. Any page with unbounded row loading or query count fails
regardless of the current small dataset.

## 13. Operational verification

- Start with valid configuration.
- Fail startup on invalid/missing security configuration.
- Liveness/readiness transitions during PostgreSQL outage and recovery.
- New-login behavior during Authentik outage.
- Graceful shutdown under in-flight reads and writes.
- Deploy previous-to-current release.
- Failed migration handling.
- Application rollback with schema compatibility.
- Backup creation, off-host retention, and restoration into a clean instance.
- Caddy site, TLS, root route, asset, cookie, and callback behavior.
- Log and request-ID usefulness during a simulated failure.
- Parse and normalize the exact Compose file with the pinned target Compose
  implementation; reject missing interpolation inputs and secret files.
- Build the application image from the exact release archive, verify its
  labels and binary identity, and prove no secret values appear in image or
  container configuration.
- Inspect the live application container for nonroot execution, read-only root,
  dropped capabilities, `no-new-privileges`, healthy status, journald logging,
  host networking, and the production loopback-only listener.
- Recreate only the application service while preserving the PostgreSQL
  container identity, image digest, data-mount source, and database state.

## 14. Coverage policy

Changed behavior, edge cases, regressions, permission failures, and failure
paths are covered wherever practical. The target is complete relevant coverage
of the issue surface, not a cosmetic repository-wide percentage.

Coverage gaps require:

- Exact untested behavior.
- Why automation is impossible or wasteful.
- Manual evidence, if any.
- Risk and owner.
- Follow-up issue or explicit acceptance.

## 15. Release gates

### Alpha.1

- PRD alpha acceptance boundary passes.
- Auth and access matrix passes for implemented surfaces.
- Fresh migration and deployed smoke test pass.
- No known critical secret, authentication, authorization, or data-loss defect.

### Beta.1

- All version 1.0 functional requirements implemented.
- Complete leakage inventory passes.
- Accessibility core-flow review complete.
- Upgrade and initial backup/restore rehearsal pass.
- Known limitations are published.
- Complete pages retain exact `Powered by GOTTH Board` attribution and the
  `https://github.com/gotthboard` target under a non-product configured site
  name/home URL; HTMX fragments remain footer-free.
- Area administration create/detail/group forms render explicit control
  borders, padding, full-width/min-width constraints, labels, focus-visible
  treatment, mobile stacking, and bounded desktop grids. Browser evidence at
  320 CSS pixels and desktop width proves no horizontal overflow or overlap.

### RC.1

- Full requirement traceability has evidence.
- No open critical/high defect.
- Security review, dependency review, and release rehearsal complete.
- Release artifact and migration sequence frozen.

### Stable 1.0

- RC evidence remains valid after fixes.
- Production deploy and rollback plan approved.
- Restore rehearsal and operator handoff complete.
- Owner confirms the deployed candidate works.
- Working commit and artifact digest recorded as the known-good reference.

## 16. Evidence record template

```text
Requirement(s):
Release/issue:
Commit:
Artifact digest:
Environment:
Command or procedure:
Expected result:
Actual result:
Evidence location:
Coverage gaps:
Reviewer:
Timestamp:
```

## 17. AN-01 reports and moderation evidence

| Field | Value |
| --- | --- |
| Requirements | `MOD-001` through `MOD-004`, `MOD-007`, `MOD-008` |
| Issue | `AN-01` |
| Source branch | `feature/alpha-n-reports-moderation` |
| PostgreSQL | 17.10, disposable container pinned to `postgres@sha256:a426e44bac0b759c95894d68e1a0ac03ecc20b619f498a91aae373bf06d8508d` |
| Environment | `development`, exact source copied from the feature worktree |

The following gates passed on 2026-09-06:

- constrained focused tests on `agenthost` for moderation, store, HTTP UI, and
  migrations;
- `go test -mod=readonly -race -tags=integration -p=1 ./...` against the
  disposable PostgreSQL 17.10 instance;
- `go vet -mod=readonly ./...`;
- `go test -mod=readonly -tags=integration -p=1 -coverprofile=... ./...`;
- deterministic sqlc, Templ, and Tailwind regeneration, including the expected
  stylesheet SHA-256
  `0a190b2010937a7f775df82d43ddf8882f662d156338beeff7d6e71762a0c46c`;
- `gofmt`, `git diff --check`, and repository integrity checks.

The database workflow covers all three target kinds, hidden and self-target
denial, active-report uniqueness and cap behavior, queue ordering, authority
revocation, every report transition, every extended action, fixed redaction
state, tree-page target links, and atomic audit creation. Ordinary and HTMX
HTTP paths cover the same successful destinations and error mappings.

Tagged statement coverage was 74.4% for `internal/httpui`, 83.1% for
`internal/moderation`, 97.4% for `internal/store`, 41.4% for generated
`internal/store/db`, and 100% for migrations. The explicit gaps are defensive
malformed-return branches in the service and HTTP adapters and mechanically
generated sqlc scanner permutations. Forcing each with production-shaped
PostgreSQL would add test machinery without improving the exercised authority,
transaction, conflict, or leakage boundaries. Deployed Authentik browser
acceptance and manual keyboard/mobile review remain release-gate evidence; no
local result is represented as that evidence.

## 18. Alpha.3 evidence

### 18.1 Native-toolbar IME evidence

The exact clean executable-and-methodology commit
`01c0247a05d65e2e2dd08483d1202cd4ca4ff01b`, tree
`20986e97fdd8c8cfd1deabe5745810d0595099fb`, was tested from a detached
bundle clone on `development`. Node 26.7.0 passed all 19 deterministic toolbar
tests. Chromium 151.0.7922.71 was then driven through the DevTools protocol
without a browser-testing dependency: `Input.imeSetComposition` began a real
composition in the focused textarea and physical mouse press/release events
activated Bold. The pointer gesture left the browser-owned value, active
element, and selection byte-for-byte unchanged and did not emit
`compositionend` or textarea `blur`. After `Input.insertText` committed the
composition, native Tab and Space activation formatted the selected text and
returned focus to the textarea. A second physical gesture was moved and
released away from the button, proving no click, composition end, blur, value
change, or selection change; after the release timer retired that gesture,
native Tab plus Space applied Bold and Tab plus Enter removed it. The test also
requires observed `pointerdown` and `click`. Deterministic tests force the
adverse pointerdown-to-compositionend-to-mousedown-to-click order and cover
pointer cancellation, lost capture, mouse fallback, multiple pointer
identities, button/editor isolation, HTMX cleanup, and later independent
gestures, including a second pointer acting without consuming the first
pointer's guarded trailing click. The generated toolbar equals its source
byte-for-byte at SHA-256
`9b94e2d14953039596b28abd1bf40cda34ebc0fcd910204606ca0f3862b36848`.

### 18.2 Renderer migration evidence

The canonical Linux/amd64 reproduction command is
`GOTTH_BB_EVIDENCE_OUTPUT=/new/absolute/path scripts/alpha3-evidence-launcher rerender`.
The committed statically linked launcher is the only supported entry point;
direct `bash scripts/verify-alpha3-rerender-performance.sh` invocation is
rejected. Before Bash exists, the launcher retains only the database URL,
new evidence path, and optional container name, clears the environment, fixes
the repository working directory and Git configuration boundary, and opens
both live runners plus their shared custody library with `O_NOFOLLOW`. Each
single opened stream is copied and hashed into a sealed Linux memfd against
compiled exact SHA-256, byte-size, and mode identities; Bash executes the
selected runner from inherited file descriptor 3 and sources the shared
library from inherited file descriptor 4. No checked pathname is reopened for
execution. It rejects symlinks, special
assume-unchanged or skip-worktree index flags, repository/worktree settings
that can hide status changes or alter archives, and nonempty
`$GIT_DIR/info/attributes`; every custody Git command explicitly disables
fsmonitor, untracked-cache, and ignore-stat behavior. Only after those checks
and an exact clean-tree check through fixed `/usr/bin/git` does it start
`/usr/bin/bash --noprofile --norc -p /proc/self/fd/3` with an explicit
environment allowlist and the two sealed descriptors.
The runner requires that launcher as its live direct parent through `/proc`,
rechecks static linkage, and records its resolved path and SHA-256. It then
captures HEAD and tree, writes exactly one deterministic committed archive,
and extracts that captured archive into private scratch. Compilation occurs
only inside the extracted tree, never inside the live worktree. The compile
uses fixed `/usr/bin/go` under `env -i`, `GOENV=off`, `GOWORK=off`, empty
`GOFLAGS`, the exact checksum-verified Go 1.26.6 toolchain, disabled CGO,
fixed Linux/amd64/v1 targets,
isolated build/module/temp caches, and checksum-verified public module
downloads. The measured binary also starts under `env -i`. Because the entry
point is static and Bash starts only after environment clearing, `BASH_ENV`,
`ENV`, `SHELLOPTS`, `BASHOPTS`, `CDPATH`, imported shell functions, and
dynamic-loader variables cannot execute or control the runner before custody.
The script records both the bootstrap and selected
compiler paths and digests, refuses
a dirty source tree, and requires the same HEAD/tree/archive identities plus a
clean tree after the measured process exits. It requires
`GOTTH_BB_TEST_DATABASE_URL`,
requires the evidence output to be a new file in an existing directory, and
preserves the complete bounded test transcript plus identity footer there
before its scratch directory is removed. It verifies that
`GOTTH_BB_POSTGRES_CONTAINER` (default
`gotth-bb-alpha3-totality-pg`) uses both the exact configured image reference
and image ID
`postgres@sha256:a426e44bac0b759c95894d68e1a0ac03ecc20b619f498a91aae373bf06d8508d`,
is running with exactly one loopback-published PostgreSQL endpoint, and that
the test URL's authority names that exact endpoint. The selected test parses
the same URL with pinned pgx v5.10, admits only one exact `sslmode=disable`
query setting, and requires the effective pgx host/port to match with no TLS or
fallback target. Query `host`/`port`, multiple-host, service/service-file,
target-session, TLS-fallback, keyword-setting, and alternate-authority cases
are rejected in focused tests. The script independently reads the database
system identifier through `docker exec` against the inspected container; the
measured SQL connection must return that exact identifier while also requiring
`server_version_num = 170010` and recording its address, port, and ephemeral
test database. The script compiles the committed
integration test, runs it once with `GOMAXPROCS=4`, and samples the test
process's `VmRSS` from `/proc` every 50 ms. It prints the before/after source
identities and cleanliness, extracted-archive execution mode, OS, Go bootstrap
and compiler digests/version/environment, container endpoint/image identity, container and
live-SQL system identifier, fixture digests, transaction elapsed time, result
state, and sampled peak RSS without printing the database URL. A committed
negative test first proves an unsupported direct Bash invocation executes a
malicious `BASH_ENV` before it can reject the caller. It then invokes both
canonical launcher modes with the same payload, imported `dirname`/`sudo`/
`docker` functions, `SHELLOPTS=xtrace`, hostile shell paths and loader values,
and a secret URL sentinel, and requires no payload/function marker, spoofed
value, evidence file, or secret output while proving that the exact attested
runner and library entered Bash. Separate disposable-clone cases modify the
runner or library behind assume-unchanged and skip-worktree bits, install a
hostile repository fsmonitor, retain exact bytes with each forbidden bit, and
add a highest-precedence `info/attributes`; all must fail before Bash, secret
exposure, payload execution, or evidence creation. The test also rebuilds the
static launcher byte-for-byte from the captured committed archive. A
synchronized Go regression replaces a runner pathname after capture and proves
the sealed original bytes, not the replacement, are what Bash executes. The admitted
launcher SHA-256 is
`9fcf2d27f3e420b41c4b02e5d5acd787cf23e1c51eab1c1cdd486a15a524dba9`.
Finally, the test places
syntax-invalid `_test.go` files behind both `.gitignore`
and `.git/info/exclude`, supplies hostile ambient Go workspace, overlay,
toolchain, cache, target, and compiler settings, and proves only the committed
archive compiles and runs.

The fixture is exactly 100 copies of
`strings.Repeat("- [x]\n", 65536/len("- [x]\n"))`, each paired with the
byte-for-byte Goldmark v1.8.5/Bluemonday v1.0.27 p1 output. The test applies
migrations 1 through 6, inserts one coherent topic and those 100 posts, applies
migration 000007, times one real `runBatch(..., 100)` transaction, and requires
100 p1-preserved markers, 100 exact HTML matches, `converted_count=100`, and
`completed_at IS NULL`. It then runs the mandatory empty transaction and
requires exact writer-constraint validation and completed state. This is a
measured dense-task fixture, not a proof that it is the maximum over every
valid p1 source.

The exact clean executable-and-methodology commit tested on `development` on
2026-09-07 was
`01c0247a05d65e2e2dd08483d1202cd4ca4ff01b`, with tree
`20986e97fdd8c8cfd1deabe5745810d0595099fb` and deterministic source-archive
SHA-256
`fb6daa4d48da5fb9df02ae1d48f151417e8b49f0c86eca756e47a68ca8fef878`.
The fixture source was 65,532 bytes with SHA-256
`5949974d253f9c125c1c299c557d17d3a501963959d987d927d653af04c57e2c`;
its exact p1 HTML was 141,997 bytes with SHA-256
`8379d153b1eeec05b8cea9b844ea24b612cbe32aa1d0ce9a9cb050ba1ff57a1a`.
The real 100-row transaction took `20.644660958s`; all 100 rows were
p1-preserved with exact HTML, `converted_count` became 100, the subsequent
empty batch validated the exact writer constraint and completed the singleton,
and the test process's sampled peak RSS was 74,368 KiB. The run used
`GOMAXPROCS=4`, Go 1.26.6, Linux 7.1.5 x86-64, PostgreSQL 17.10, and the exact
image reference and ID above. The complete retained transcript is
[`docs/evidence/alpha3-dense-01c0247.txt`](evidence/alpha3-dense-01c0247.txt),
SHA-256
`870ee5a73e5a64387a3a2538ac17732b52598ba51624243e4a3adb28aaf87bb6`.
This documentation-and-evidence commit is the direct child of the tested
executable commit; it changes no executable source, fixture, or methodology.

The separate population-scale reproduction command is
`GOTTH_BB_EVIDENCE_OUTPUT=/new/absolute/path scripts/alpha3-evidence-launcher population`.
It applies the same extracted-archive build, empty build/run environment,
before/after source-custody, new retained-output, exact-image, exact loopback
endpoint, live-SQL-identity, environment-identity, stable system-identifier
equality, and 50 ms test-process `VmRSS` sampling gates. Its
committed integration fixture creates 25,000 coherent
ordinary p1 posts as 1,000 full 25-post topics, then verifies every topic's
exact post count, first/latest/root relationships, counters, parent, and thread
path before measurement. Fixture loading is explicitly outside the timed
release phases. The test separately records complete preflight time, 251
row-batch transactions, the initial schema/ledger transaction, and all 254
query round trips; schema time; 250 ordinary conversion batches;
251 instrumented mutation selections returning exactly 25,000 unique rows
without revisiting a committed prefix. With PostgreSQL
`plan_cache_mode = force_generic_plan`, it also performs 251
`EXPLAIN ANALYZE` probes in each phase across that phase's exact initial and
cursor selection shapes.
Both phases must examine exactly the 25,000 returned rows through `posts_pkey`,
and every cursor-bearing plan must retain its direct primary-key lower-bound
index condition. The test then records the final whole-table
validation/completion transaction, total re-render and release time, exact
final row/state/cursor counts, and sampled test-process RSS. This is a
representative 1,000-page measurement point, not a universal timing bound or
capacity promise.

The exact clean executable-and-methodology commit for this population run was
`01c0247a05d65e2e2dd08483d1202cd4ca4ff01b`, with tree
`20986e97fdd8c8cfd1deabe5745810d0595099fb` and deterministic source-archive
SHA-256
`fb6daa4d48da5fb9df02ae1d48f151417e8b49f0c86eca756e47a68ca8fef878`.
The 25,000-row fixture used 35-byte source with SHA-256
`dc0767673dc19d4fcd263526028c93f097464a955a8d1f520a1663b8f9c4ce98`
and 56-byte exact p1 HTML with SHA-256
`3a0d36f5ae4a5939c8d8f6fed339ec77d41d697a60c17ffbe306296eabb54970`.
Complete preflight took `1.715047016s`, schema apply took `47.585931ms`,
250 conversion batches took `15.350866698s`, and final full-table validation
plus completion took `27.560897ms`. Total re-render time was `15.378427595s`;
the measured release path was `17.141062512s`; sampled test-process peak RSS
was 20,632 KiB. The exact final state contained 25,000 current rows,
`converted_count=25000`, `last_processed_post_id=25000`, one non-null
completion time, and a validated writer
constraint. The environment and pinned PostgreSQL/Go identities were identical
to the dense compatibility run above. The complete retained transcript is
[`docs/evidence/alpha3-population-01c0247.txt`](evidence/alpha3-population-01c0247.txt),
SHA-256
`f07c2a27b8cf35eb2a78318df7674661b044d13a6f483a386c8075d1e2d80d54`.

The same exact `01c0247` source passed deterministic generation, the
adversarial custody suite, `go vet`, full non-integration race/coverage, full
PostgreSQL 17.10 integration race, and integration coverage on `development`.
The integration coverage run reported 93.7% for `internal/render`, 56.7% for
`internal/rerender`, and 100% for both `internal/readiness` and `migrations`.
This documentation-and-evidence commit changes no executable source, fixture,
or evidence methodology.

## 19. AN-02 search and recent-activity evidence contract

AN-02 evidence is admitted only from one exact implementation tree whose
migration, generated SQL/Templ/static files, application binary, test helpers,
and retained transcripts are hash-bound. Passing smaller fixtures does not
substitute for the required population/plan gate.

### AN-02-02 representative plan checkpoint

The exact executable and plan-methodology commit
`9034e63c4953d5a21c55b02a5fe0d371064a5d3f`, tree
`2ac6f53defc8b0757c35a1fd454126fb1a33d5ec`, populated 25,000 topics and
25,000 posts across public and group-restricted areas on PostgreSQL 17.10.
The corpus includes common and rare terms plus a one-percent author
distribution. The exact generated search, cursor-bearing activity, and direct
post statements were prepared and retained through
`EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON)` under both
`force_custom_plan` and `force_generic_plan`.

The custom author shapes used both exact partial author indexes; the custom
rare-term shape used the topic GIN index. Both plan modes retained the 51-row
search limit and complete area/topic/post authorization tree. Both activity
plans used `posts_activity_current_idx` with a direct strict tuple index
condition, and both direct-post plans started at `posts_pkey`. This is a
representative AN-02-02 plan checkpoint, not the required AN-02-04
100,000-topic/1,000,000-post admission corpus and not a universal latency
promise.

The command was
`GOTTH_BB_RUN_DISCOVERY_PLAN_EVIDENCE=1 go test -tags integration -run
TestDiscoveryPlansOnPostgreSQL17 -count=1 -v ./internal/store/db` on
`development` with Go 1.26.6 and PostgreSQL 17.10 Alpine image ID
`sha256:742f40ea20b9ff2ff31db5458d127452988a2164df9e17441e191f3b72252193`.
The complete retained transcript is
[`docs/evidence/an02-02-plans-9034e63.txt`](evidence/an02-02-plans-9034e63.txt),
SHA-256
`e8750a76a06a6d31e8afb5300037d3f6e2fc4d66ef3f4d9385e504138a6c6bfe`.

### 19.1 Functional and authorization matrix

Automated unit, HTTP, and PostgreSQL 17 tests shall cover:

- strict request keys/cardinality/wire bound, canonical links, NFC, web-search
  terms/phrases/`OR`/negation, 31-node semantic bound, author/area/date/page
  grammar, maximum-date handling, and fixed errors that never echo input;
- typed topic/post results, root deduplication before the 51 fence, per-kind
  filters, deterministic equal-time ties, `0..50`/`50+`, rank only within 50,
  page 1/2 behavior, and concurrent-snapshot duplication/omission semantics;
- visitor, member, group hit/miss, moderator, and administrator results proving
  authorization precedes match identity, limit, count, rank, excerpt,
  continuation, and direct-post fields;
- deleted/redacted posts, deleted/hidden topics, title independence from the
  root body, and a visibility distribution where restricted activity cannot
  suppress older public rows or alter terminality;
- activity 25/26 boundaries, strict equal-timestamp keysets, insertion,
  deletion, authority revocation between pages, and the closed absent-or-one-
  cursor query grammar including unknown/duplicate/empty/overlength cases;
- cursor exact-length/alphabet/re-encoding, integer endpoints, unknown key,
  tamper, constant-time comparison path, issue/key windows, expiry/future skew,
  audience role/group change, rotation overlap/removal, malformed keyring, and
  database-time authority across ordinary restart;
- direct post canonical ID, primary-key-started authorization, projection
  independence, no-query grammar, no tree enumeration, and indistinguishable
  missing/deleted/redacted/inaccessible `404` behavior; and
- full-page/HTMX parity, base path, navigation without JavaScript, keyboard/
  screen-reader semantics, `private, no-store`, 256-KiB envelope, and no partial
  response.

Sanitizer goldens shall prove visible-text/excerpt output includes only text
nodes in admitted render order and that raw Markdown, HTML, comments,
attributes, URL destinations, event/style data, and sanitizer-removed bytes
cannot match or appear. Block-boundary spaces, Unicode whitespace collapse,
trim, NFC, first-300-rune truncation, redaction empty-vector behavior, and 25
maximum-size rendered-post inputs are explicit cases.

### 19.2 Migration, plan, and resource evidence

Fresh and upgrade integration shall cover preflight failure before 000008,
every topics/posts phase and nullable-cursor interruption boundary, batch size
100, two concurrent runners, atomic cursor/count progress, trigger narrowing,
writer atomicity, six-check validation, five exact partial indexes, completion
idempotency, interrupted/repeated/concurrent explicit topic/post `ANALYZE`,
every illegal singleton phase/cursor/count/time shape, completion count/max-ID
reconciliation, readiness catalog drift, minor-PostgreSQL full-corpus comparison,
and the honest forward/restore rollback boundary. Index-build heap passes and
lock/I/O exposure are measured, not described away. Completion measurements
separately cover explicit analysis, the zero-stale full-table oracle, and every
constraint-validation scan/`SHARE UPDATE EXCLUSIVE` lock.

The generated admission corpus contains at least 100,000 topics and 1,000,000
posts across public, authenticated, groups, hidden, deleted, redacted, common-
term, rare-term, author, and activity-skew cases. Corpus size is test evidence,
not a production quota. With PostgreSQL 17, retain `EXPLAIN (ANALYZE, BUFFERS,
FORMAT JSON)` evidence for exact current-vector, author, date/area, common-term
fallback, activity, and direct-primary-key shapes under custom and forced-
generic plans. Unsafe plans reject the implementation; observations do not
become fictional universal latency guarantees.

Two concurrent discovery requests shall coexist with ordinary reads and
publication while measurements record latency, allocations, RSS, temporary
I/O, connection cleanup, cancellation, two-permit saturation, slow clients,
and fixed failure responses. Full/race/population/browser work runs on the
designated development host, not inside the gateway cgroup.

### 19.3 Admission gates

The exact final candidate must pass SQL generation, Templ/static regeneration,
gofmt, vet, focused unit/HTTP, PostgreSQL integration/race, migration,
population/plan/resource, browser-through-Caddy, secret/log-redaction,
repository-integrity, and release-artifact reproducibility gates. Evidence
records the exact commit/tree, commands, environment, versions, checksums,
result, and explicit gaps. Two fresh independent cold reviews must both be
CLEAN on that same exact state before handoff.

### 19.4 AN-02 integrated admission result

The exact executable and admission-methodology commit was
`1805fb655e05dcf6f7f9528d2fadbf8928b7ed5c`, tree
`3913e200a0ea5e5dc1f7b33d1dcc3f4d6e966c19`. Its deterministic source archive,
with prefix `gotth-bb-1805fb6/`, has SHA-256
`559a3494969d3c52826e3d699f3795992a46bdddfe2e5e4934ab355827b730f8`.
The designated `development` host used Go 1.26.6, Node 26.7.0, npm 12.0.2,
PostgreSQL 17.10 image
`sha256:742f40ea20b9ff2ff31db5458d127452988a2164df9e17441e191f3b72252193`,
Caddy 2.11.4, and Chromium 151.0.7922.71.

Exact command forms were:

```text
GOTTH_BB_TEST_DATABASE_URL=<redacted-loopback-test-dsn> GOTTH_BB_RUN_DISCOVERY_PLAN_EVIDENCE=1 go test -mod=readonly -tags integration -run '^TestDiscoveryPlansOnPostgreSQL17$' -count=1 ./internal/store/db
GOTTH_BB_TEST_DATABASE_URL=<redacted-loopback-test-dsn> GOTTH_BB_RUN_AN02_ADMISSION_EVIDENCE=1 go test -mod=readonly -tags integration -run '^TestDiscoveryPlansOnPostgreSQL17$' -count=1 -v ./internal/store/db
PATH=<pinned-node-26.7.0-and-npm-12.0.2>:$PATH make verify
GOTTH_BB_TEST_DATABASE_URL=<redacted-loopback-test-dsn> go test -mod=readonly -tags integration -p=2 -race -covermode=atomic ./...
GOTTH_BB_BROWSER_CADDY=1 go test -mod=readonly -tags integration -run '^TestDiscoveryBrowserThroughCaddy$' -count=1 -v ./internal/httpui
go run -mod=readonly ./cmd/package --version 1.0.0-alpha.2 --commit 1805fb655e05dcf6f7f9528d2fadbf8928b7ed5c --goos linux --goarch amd64 --output <fresh-release-a>
go run -mod=readonly ./cmd/package --version 1.0.0-alpha.2 --commit 1805fb655e05dcf6f7f9528d2fadbf8928b7ed5c --goos linux --goarch amd64 --output <fresh-release-b>
diff -ru <fresh-release-a> <fresh-release-b>
git archive --format=tar --prefix=gotth-bb-1805fb6/ 1805fb655e05dcf6f7f9528d2fadbf8928b7ed5c | gzip -n > gotth-bb-1805fb6.tar.gz
```

The PostgreSQL DSN was loopback-only and test-owned. Its disposable credential
and absolute scratch paths are deliberately redacted; no production secret or
endpoint was used.

The generated corpus contained exactly 100,000 topics and 1,000,000 posts:
33,333 public, 33,334 authenticated, and 33,333 group topics; 990 hidden and
99 deleted topics; 1,978 deleted and 991 redacted posts; 100 rare-term topics,
1,003 rare-term posts, and 10,000 distinct post timestamps. Population and
analysis took 31.546 seconds. The resulting database was 596,850,355 bytes;
topics consumed 49,192,960 bytes and posts 538,591,232 bytes. The complete
custom- and forced-generic `EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON)` plans
retained the authorized 51-row search fence with its area/topic/post/group
filters structurally beneath the exact candidate node, both author indexes for
custom author shapes, both topic and post GIN indexes for the custom rare-term
shape, the strict activity tuple condition on `posts_activity_current_idx`, and
the direct-post start on `posts_pkey`.

The same run overlapped exact search and activity statements with an ordinary
topic read and the production topic/first-post publication statement. They
completed in 1.700 seconds, 36.434 milliseconds, 6.282 milliseconds, and
62.233 milliseconds respectively. Connection count returned from one to one,
cancellation failed closed, process RSS rose from 17,776 KiB to 21,068 KiB,
and PostgreSQL reported nine temporary files totaling 294,707,167 bytes after
all plans and coexistence work. These are observations on the evidence host,
not universal latency or capacity promises.

The exact commit also passed deterministic generation, Alpha.3 evidence
custody, gofmt, vet, repository-wide race/coverage, full PostgreSQL 17
integration/race including migration and readiness, browser-through-Caddy,
secret/log-redaction, clean-tree and `git fsck --no-dangling` checks. Two
independent Linux/amd64 package runs produced byte-identical artifacts with
SHA-256 `36d7ce67aa7b3392d4ec33ff67b69dfdcd264b6845f869a91a85192c8173bb0a`.

Retained transcripts:

- [`an02-04-admission-1805fb6.txt`](evidence/an02-04-admission-1805fb6.txt),
  SHA-256 `eab671bff1a46049e5acbd02df8fd3e59f65a01cd420dba7bf336427f7c10be0`;
- [`an02-04-verify-1805fb6.txt`](evidence/an02-04-verify-1805fb6.txt),
  SHA-256 `c0a311e50513c964af6e8730b79c76664b8f026e8423f6dddfdb5c281381028f`;
- [`an02-04-integration-race-1805fb6.txt`](evidence/an02-04-integration-race-1805fb6.txt),
  SHA-256 `a85b5c1ddd8cc233e21a0eede4404e5ec4c74516a30fe21d188f4990659ca2c1`;
- [`an02-04-browser-1805fb6.txt`](evidence/an02-04-browser-1805fb6.txt),
  SHA-256 `5d35fc7db1313cc10a63e97fe3989545a20a96451bcc63089038a3ed8b66760e`;
  and
- [`an02-04-release-1805fb6.txt`](evidence/an02-04-release-1805fb6.txt),
  SHA-256 `1cfed2b585258c1de53eaad4c975a629a50134f52b1ec6f1adeb0e55bbfcd70b`.

The evidence contains no universal performance guarantee. The generated
corpus and release artifacts were disposable verification state; the
transcripts and exact source identities are the retained record.

## 20. AN-03 unread-state evidence contract

AN-03 evidence distinguishes read-state correctness from topic access. A
marker is private preference state and never evidence that an actor may read a
topic.

### 20.1 Functional and authorization matrix

Automated unit, HTTP, and PostgreSQL 17 tests shall cover:

- the closed `new`/`unread`/`read` state table for no eligible posts,
  own-post-only topics, absent marker, marker below/equal/above eligible head,
  malformed persisted rows, and the exact combined board-index count;
- visitor output proving no marker join, state field, count, action, form, or
  private cache variation; member, matching/nonmatching group, moderator, and
  administrator output over public/authenticated/group/hidden topics;
- authorization before topic identity, marker existence, readable-head test,
  count, state, first-unread target, tree ordinal, redirect, and terminality,
  including a distribution where restricted topics cannot change an authorized
  area's count;
- per-topic area-page indicators, exact board-index counts, zero/dense/sparse
  markers, more than one matching group without row multiplication, empty
  areas, and the fixed 25-topic page boundary;
- canonical path and no-query grammars before session/body/database work,
  login/revalidation behavior, exactly one body-free `X-CSRF-Token` header or
  exactly one `_csrf` URL-encoded field and no other body field, fixed
  `400`/`403`/`404`/`503`, base-path URLs, mark-read ordinary empty 303 and HTMX
  empty 204 `HX-Location` equivalence, and no echoed IDs or state;
- first unread at the root, equal-time/number boundaries, deleted/redacted
  gaps, tombstone ancestors, staff tree shape, page 1, page 10,000, the 250,001
  direct-post fallback, no-unread root redirect, and concurrent access
  revocation between requests; ordinary empty 303 and HTMX empty 204
  `HX-Location` path/fragment/history equivalence; and
- authenticated full-page/HTMX `private, no-store`, retained visitor cache
  behavior, no marker-derived shared-cache entry, no-JavaScript controls,
  visible focus, semantic state text, keyboard activation, and screen-reader
  names.

Tests inspect `topic_reads` before and after every successful and failed GET to
prove that board, area, topic, post, search, activity, and first-unread reads
never write. Cross-site-safe semantics are not inferred from SameSite cookies;
only the CSRF-protected mark-read POST may advance state.

### 20.2 Mutation, deletion, and concurrency matrix

PostgreSQL integration and race tests shall cover first insert, equal/lower/
higher retry, two devices in both commit orders, a new post before and after
the mark snapshot, cancellation, statement failure, unknown commit inspection,
and exact `read_at` no-change/change behavior. No client field may select a
watermark or another user.

Topic and reply publication tests prove zero marker writes for an author with
no marker, a marker behind or ahead of the new post, concurrent reply
allocation, rendering failure, authorization failure, and the existing
no-retry unknown-outcome boundary. State and first-unread tests prove the
current actor's own posts are excluded. Edit, preview, delete, restore, redact,
moderation, move, direct-post, search, and activity tests also prove zero marker
writes.

Deletion tests cover unread-only post deletion/redaction, tombstone ancestors,
restore above and below the marker, hard post purge without marker decrement,
topic hide/restore, area/group access removal/restoration, topic soft deletion,
and user/topic cascade deletion. Suspension tests prove immediate session
failure, visitor-only public reads, no personalized state/control, and retained
dormant storage; mute tests preserve state/control while publishing remains
denied. Dormant marker state must not leak or grant access.

### 20.3 Migration, plans, and resources

Fresh and upgrade migration tests cover exact head 000009, finite `read_at`,
regular partial-index construction, transaction rollback, unknown migration
outcome, idempotent rerun, readiness, and the absence of backfill or fabricated
marker rows. Legacy `infinity`/`-infinity` must abort the whole transaction,
leave the ledger at 000008, and require inspected forward repair before retry.
Retain relation size, elapsed time, lock mode/wait, buffers, and I/O for the
`posts` index scan and `topic_reads` constraint validation.

The representative checkpoint contains at least 25,000 topics and 250,000
posts; integrated admission reuses at least 100,000 topics and 1,000,000 posts
across public, authenticated, group, hidden, deleted, redacted, shallow/deep
trees, sparse/dense markers, own-post-only and newest-own-post scan cases, and
0/1/25/26/250,000/250,001 boundaries. Corpus sizes are evidence points, never
publication quotas.

Retain `EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON)` under custom and forced-
generic plans for authenticated board counts, bounded area states,
first-unread selection/tree placement, and mark-read boundary selection.
Structural assertions prove area/topic authorization below every identity or
aggregate boundary, group membership as a semi-join, unique topic contribution,
and `posts_topic_unread_visible_idx`, including its `author_id` payload, on the
targeted visible-post shapes.
Planner observations do not become hints or universal latency guarantees.

Measure allocations, RSS, temporary I/O, database connections, cancellation,
five-second read/first-unread and two-second mark-read deadlines, transaction-
local statement/lock timeouts, and coexistence with topic/reply publication.
First-unread must cap tree placement at 250,001 identities; area results at 25
topics; mark-read at one marker row. The exact board count and actor-excluding
post scans are openly population-dependent.

### 20.4 Admission gates

The exact final candidate must pass SQL/Templ/static generation, gofmt, vet,
focused unit/HTTP, PostgreSQL integration/race, migration, population/plan/
resource, authorization-leakage, browser-through-Caddy, CSRF/cache/log-
redaction, repository-integrity, and release-artifact reproducibility gates.
Evidence records exact commit/tree, commands, environment, versions, checksums,
result, and explicit gaps. Two fresh independent cold reviews must both be
CLEAN on the same exact state before handoff.

## 21. AN-04 administration-completion evidence contract

AN-04 evidence treats every administrator projection and mutation as a private
authorization boundary. Possession of a rendered form, target ID, revision, or
prior administrator session is never authority.

### 21.1 Functional and authorization matrix

Automated unit, HTTP, and PostgreSQL 17 tests shall cover:

- area create, display-name rename, equal-order ID tie-breaking, reorder,
  public/authenticated/group visibility, paged zero/one/many group restrictions,
  normal/read-only/archive, explicit restore to both allowed modes, immutable
  slug, raw-keyset behavior across concurrent reorder, initial group on
  transition, single grant/revoke, last-group protection, no-op, stale/
  overflowing revision, missing group, audit failure, and every publication
  path while archived; bounded description/group digests and streaming O(1)-
  auxiliary hashing at high mapping cardinality;
- account list boundaries 0/1/50/51, canonical continuation, account detail,
  member/moderator/administrator role labels, active/future/expired/indefinite
  suspension state, paged zero/one/many memberships, 50/51 group-page
  boundaries, and no contact, identity, session, IP, or user-agent columns;
- role changes across every distinct role pair; stale/no-op/self targets;
  suspended/missing/malformed actor or target; target session revocation;
  OIDC login preserving the result; final-active-administrator protection; and
  concurrent bootstrap, role, and suspension operations in both lock orders;
- group create/rename, Unicode/NFC/control/length/case-fold boundaries,
  duplicate names, 50/51 pagination, stale/no-op/overflowing rename, single
  membership grant/revoke/no-op/stale/missing cases, exact one-row audit,
  rollback on any audit failure, and immediate next-
  request area-access changes;
- default and updated site presentation, separate shell/rules/edit projections,
  one singleton round trip for rules/settings pages, proof that ordinary shells
  do not select/copy rules bodies, every closed
  theme, rejected arbitrary theme/CSS/HTML/URL and name/description controls,
  GFM and sanitizer fixtures, source/HTML size
  boundaries, exact empty source/HTML sentinel, byte-for-byte acceptance of
  every nonempty source admitted by `render.RenderMarkdown` including
  decomposed Unicode, rejection of every other blank source, stale/no-op/
  overflowing settings, digest-only bounded audit,
  renderer/audit failure, two-instance next-read propagation, empty/nonempty
  public rules, and no raw Markdown or private
  revision metadata in public output; and
- exact dashboard reconciliation for empty and populated relations, all role
  and effective-suspension buckets, deleted topics/posts, redacted posts,
  open/in-review/resolved/dismissed reports, cancellation, timeout, malformed
  counts, and zero execution for visitor/member/moderator requests.

Tests inspect `moderation_actions`, target rows, group mappings, settings, and
sessions before and after successful and failed requests. A committed mutation
must have its exact audit state; a failed mutation must have neither state nor
audit/session side effects. Unknown commit tests report uncertainty and inspect
before any explicit retry. Application tests cover missing, blank, Unicode-
space-only, multiline, control-bearing, decomposed Unicode, boundary, and
oversized reasons. Direct database tests cover NULL requirements (including
`reinstate_user`), byte boundaries, ASCII-space trim, and POSIX controls without
pretending PostgreSQL duplicates the application's Unicode-space rules.

### 21.2 HTTP, privacy, and accessibility matrix

Route tests cover exact canonical path/query grammar before session/body/
database work, login and revalidation redirects, fixed administrator `403`,
fixed missing `404`, validation `400`/`422`, conflict `409`, unavailable `503`,
shell failure preserving established `403`/`404` while successful pages become
fixed `503`, and body-nonconsumption for header-CSRF/session/path failures. Each
form proves its exact documented field set, path-only target IDs, conditional
empty/positive `initial_group_id`, the 16/64/256-KiB route-specific size limits
and worst-case URL encoding, duplicate/unknown rejection, one service call, and
no retry.

Ordinary and HTMX success must agree on canonical destination and authoritative
main-region content. All administrator full-page/fragments are
`private, no-store` and never enter shared caches. They contain no prior audit
reason, email, avatar, issuer, subject, session identifier, IP, or user agent.
Only the administrator settings edit form may contain current raw rules
Markdown; only a mutation form may contain its explicit numeric revision,
expected role/action, and conditional initial group. Target IDs occur only in
builder-owned canonical action paths, never as redundant body fields. Those
values carry no authority and never appear in public output, application logs,
or metric labels. Tests inspect all three boundaries.

Caddy/Chromium tests cover empty and populated dashboard, 51-account and
51-group continuation, area archive/restore, role change followed by target session
failure, membership-driven access loss/gain, group create/rename, site name/
description/theme and rules propagation, discoverable base-path-built rules and
administration navigation, base-path deployment, full-page and
HTMX history, settings `HX-Redirect` full-shell refresh, JavaScript disabled,
keyboard-only completion, visible focus,
semantic headings/status/errors, accessible names, and mobile/desktop widths.

### 21.3 Migration, plans, concurrency, and resources

Fresh and upgrade migration tests cover exact head 000010, default preservation,
exact singleton cardinality, current rules-renderer tuple, audit constraint and
the exact AN-04 runtime-grant delta, positive administration revisions and
atomic increments, lock/scan behavior, transaction rollback, unknown outcome,
idempotent rerun, readiness, and prior/current artifact exact-head failure.
Corrupt theme, nonfinite time, stale renderer, missing/duplicate settings, audit
constraint drift, or runtime-grant drift must fail closed.

Privilege evidence starts from the admitted pre-AN-04 restricted-role fixture,
applies the packaged delta twice, and proves settings SELECT/column-UPDATE,
group SELECT/create/rename and identity-sequence use, and membership SELECT/
insert/delete. It proves settings insert/delete/key-update, mapping UPDATE, and
DELETE on users/groups/areas/settings/audit rows remain denied. Existing
`area_groups` mapping INSERT/DELETE remains in the baseline and is re-proved
without being misreported as a new grant. Audit evidence proves exact target
columns and bounded state/digests for settings, role, membership, group, area,
and area-group actions.

Retain custom and forced-generic `EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON)` for
account list/detail, group membership, area administration, role continuity,
and dashboard counts. Structural assertions prove administrator revalidation
through the required materialized actor fence before private identity or
aggregate work, group membership as nonmultiplying sets, the 51-row account/
group fences, primary-key target starts, and expected indexes.
Planner observations do not become hints or universal latency guarantees.

The representative checkpoint contains at least 25,000 accounts, 25,000 groups,
25,000 areas, 100,000 topics, 1,000,000 posts, and report/audit rows spanning
all states. Measure elapsed time, buffers, temporary I/O, relation/index size,
allocations, RSS, database connections, cancellation, two-second statement and
250-millisecond transaction-local lock timeouts for every writer, commit-
unknown/no-retry behavior, 512-KiB response overflow, and coexistence with
login, topic/reply publication, moderation, discovery, unread reads, and mark-
read. Concurrency evidence also proves non-continuity writers do not acquire the
governance singleton and serialize safely against role/suspension through their
actor-row locks. Corpus sizes are evidence points, not content/account quotas.

### 21.4 Admission gates

The exact final candidate must pass SQL/Templ/static generation, gofmt, vet,
focused unit/HTTP, PostgreSQL integration/race, migration, population/plan/
resource, authorization-leakage, browser-through-Caddy, CSRF/cache/log-
redaction, repository-integrity, and release-artifact reproducibility gates.
Evidence records exact commit/tree, commands, environment, versions, checksums,
results, and explicit gaps. Two fresh independent cold reviews must both be
CLEAN on the same exact state before handoff.

## 22. AN-05 basic-abuse-control evidence contract

AN-05 evidence separates process-local request admission, transactional
publication accounting, and deterministic destination policy. A passing status
alone proves none of those mechanisms.

### 22.1 Configuration, proxy, and request admission

Table-driven tests cover every required AN-05 key; missing versus explicit
empty rules; canonical decimal and duration boundaries; cross-field new versus
established counts; file path length/clean/root/NUL cases; symlink, directory,
device, replacement-race, short-read, overflow, invalid UTF-8/line ending/
control, unsorted, duplicate, and 0/1/256/257-rule files. Rules cover DNS label,
IDNA, domain-rule IPv4/IPv6/trailing-dot rejection, exact-URL canonical IPv4/
IPv6, URL scheme/userinfo/port/path/query/fragment,
and already-canonical requirements. Failure messages and whole-config
formatting must contain no rules, paths, URLs, secrets, or unrelated values.

Client tests cover IPv4, IPv6, mapped IPv4, zones, malformed/missing/multiple
ports, loopback versus non-loopback peers, absent/single/duplicate/comma/
whitespace/port-bearing forwarded values, and spoof attempts. Production
loopback requires exactly one overwritten address for charged requests;
development/test direct requests reject forwarded identity. Caddy evidence
inspects the adapted configuration and proves an attacker-supplied forwarding
header is overwritten before the application sees it.
Authenticated accounts of every age receive the same request budget; tests
prove no account/session lookup is added to this pre-authentication boundary.

With a deterministic clock and digest source, limiter tests prove the exact
domain-separated HMAC-SHA-256 input and cover counts
0/1/N/N+1, exact expiry equality, negative clock movement, concurrent calls,
4,095/4,096/4,097 clients, lazy expired-entry reclamation, full-live capacity,
cached earliest-expiry O(1) rejection before the boundary, exactly one bounded
scan at the boundary, restart reset, digest collision handling, and allocation/
map cardinality. Every
charged rejection occurs before session, body read/close, router, or database
work. Exact health and content-addressed static GET/HEAD requests remain exempt;
near-miss paths, unsafe static methods, and unknown paths are charged. Race and
high-cardinality tests prove no goroutine/timer leak and no growth beyond the
configured capacity.

### 22.2 Publication transaction matrix

Fresh and upgrade PostgreSQL 17 tests cover migration head 000011, unchanged
existing account tuples, both tuple shapes, finite and positive/negative-
infinity account creation, nonfinite window start, negative/zero/overflow
count, exact columns/defaults/checks, lock/scan behavior, transaction
rollback, unknown migration outcome, readiness, idempotent rerun, and exact
restricted-role grant delta. Runtime can select and update only the two
publication columns; table-wide account update, created-at change, insert,
delete, and unrelated columns remain denied.

Service tests cover member/moderator/administrator accounts, age immediately
below/equal/above the new-account boundary, empty/active/expired window,
counts below/equal/above configured limit, database clock before account
creation, malformed stored tuples, suspension/mute/role drift, missing actor,
concurrent group grant/revocation in both lock orders, read-only/archived/
restricted/locked/missing targets, validation failure,
renderer/policy failure, insert/counter-update failure,
cancellation, lock timeout, and begin/commit failure. Before/after inspection
proves denied or failed publication has neither post/topic/counter effects;
success has exactly one counter increment and one publication in one commit.
Every lock-timeout case proves the two-second statement and 250-millisecond
transaction-local lock settings were installed before the account lock.
Time-boundary tests cover exact new-account equality, minimum and maximum
finite PostgreSQL timestamps, subtraction saturation, a window start ahead by
exactly/less than one window, a start farther ahead than one window, and a
clamped positive `Retry-After`. They prove no policy decision adds a duration
to a stored database timestamp or overflows at PostgreSQL's finite extremes.

At least `2N+2` simultaneous requests for one account at empty and near-limit
state prove exactly the configured successes and monotonic count under both
topic and reply mixes. Separate accounts do not serialize on a global lock.
Process restart retains PostgreSQL state. Commit-unknown evidence permits only
the two atomic outcomes and performs no automatic retry. A configuration
change on restart applies the new limit to the existing active count without
rewriting history. Edge evidence explicitly demonstrates the admitted possible
two-window burst rather than claiming a rolling limit.

### 22.3 Destination and HTTP matrix

Destination tests cover inline/reference/collapsed-reference links, images,
GFM automatic URLs, repeated destinations, local relative links, rejected
protocol-relative network paths, pure/mixed slash-backslash authority forms,
other ASCII-backslash destinations, anchors, mailto,
code spans, fenced code, escaped text, raw HTML, Unicode/IDNA host forms, exact
host, subdomain, sibling suffix, trailing dot, default/nondefault ports, empty/
dot/escaped paths, uppercase/lowercase percent escapes, encoded unreserved and
reserved bytes, query order/encoding, fragments, userinfo-bearing authored
links, malformed HTTP(S)-looking input, and 65,536-byte Markdown. Both domain
and exact-URL rules are exercised at 0/1/256 entries. Assertions prove the same
AST is scanned and rendered, the first rejection retains no destination/rule,
and no network or DNS call occurs.

Topic/reply/edit preview and mutation plus community-rules settings tests prove
identical allowed/blocked results and one explicit policy at every call site.
Preview/edit/settings never touch publication counters. Topic/reply validation
or blocked policy happens before
transaction begin; current account and target authorization happen before
counter mutation. Existing stored blocked content remains readable and is not
rewritten, including stored community rules. No actor role bypasses policy.

HTTP tests cover ordinary and HTMX `429` with positive integer `Retry-After`,
draft preservation after safe parsing, fixed `422` blocked-draft presentation,
fixed capacity `503`, `private, no-store`, session/revalidation/CSRF/path/query/
body ordering, and ordinary HTML with JavaScript disabled. Logs are searched
for the client address/digest, account identifiers and age, request target,
query, submitted unique sentinels, URL/domain/rule, configured counts, map
occupancy, cookies, and secrets; none may occur. Exact fixed observer class,
fixed `request-admission` or matched route, request ID, status, and retry are
present once.

### 22.4 Integrated admission

The representative gate retains existing 25,000-account, 100,000-topic, and
1,000,000-post evidence and adds 4,096 concurrent/distinct request clients and
publication contention across at least 1,000 accounts. Record elapsed time,
allocations, heap/RSS, map cardinality, database locks/connections, cancellation,
statement/lock bounds, and coexistence with authentication, reads, discovery,
unread, moderation, and administration. Browser-through-Caddy evidence covers
overwritten client identity plus keyboard/no-JavaScript blocked and rate-limit
errors at empty and nonempty base paths.

The exact final candidate passes deterministic SQL/Templ/static generation,
gofmt, vet, repository-wide unit race/coverage, PostgreSQL 17 integration/race,
migration/grant/readiness, proxy/header, concurrency, population/resource,
HTTP/cache/log-redaction, Caddy/Chromium, repository-integrity, and two byte-
identical release-package gates. Evidence records exact commit/tree, commands,
tool/database/browser/proxy versions, checksums, results, and gaps. Two fresh
independent cold reviews must both be CLEAN on that exact state before PR and
guarded fast-forward delivery.

## 23. Beta.1 evidence contract

Beta.1 binds the complete version 1.0 surface to one candidate without
pretending that RC.1's final evidence/freeze gate or stable's production
recovery posture has already passed.

### 23.1 Requirement and route closure

The Beta traceability record expands every requirement ID from PRD sections
5.1 through 5.8 into exactly one row containing:

- requirement ID and exact governing text location;
- implementation package, query, route, template, migration, or operator
  procedure;
- automated and manual evidence identifiers;
- `implemented`, `inapplicable`, or `blocked` state;
- an exact explanation for `inapplicable`, limited to a PRD non-goal; and
- the defect/reference that blocks admission when state is `blocked`.

No requirement may be marked implemented only because a neighboring grouped
traceability row passed. The generated requirement-ID set is compared to the
record in both directions so omitted, duplicate, or invented IDs fail.

The route record is derived from the exact candidate's router registrations and
augmented by middleware-owned health/static handling. It records method,
canonical path pattern, actor/session requirement, CSRF use, full-page/HTMX
mode, cache policy, database use, mutation/audit effect, and expected
missing/denied behavior. A second inventory scans form actions, links,
`HX-Redirect`, and redirect builders so an emitted endpoint absent from the
router record fails. Dynamic test-only routes and future-version surfaces are
identified explicitly rather than ignored.

### 23.2 Leakage, identity, and pre-beta security matrix

For each applicable read or mutation surface, tests exercise visitor, current
member, matching/nonmatching group member, moderator, administrator, locally
suspended, revoked-session, and Authentik-stale-session states. Restricted
fixtures cover hidden/deleted/redacted topic and post state, authenticated and
group areas, read-only/archived modes, report targets and queue summaries,
search rank/count/snippet, recent activity, unread counts/redirects, direct
post resolution, breadcrumbs/canonical metadata, administration counts, and
full-page/HTMX errors. Assertions compare status class, headers, redirect
shape, response length class where relevant, database work, and absence of
fixture sentinels; they do not normalize a real timing leak by merely accepting
different `403`/`404` results.

The exact Beta candidate also passes:

- module/dependency known-vulnerability scanning with the scanner database and
  version recorded; unavailable scanning blocks rather than reports success;
- repository-history, worktree, package, image, and resolved-container secret
  scanning without publishing matched secret material;
- method and CSRF coverage for every mutation;
- Secure, HttpOnly, SameSite, Path, CSP, frame, MIME, referrer, and cache-header
  inspection through Caddy;
- OIDC issuer/audience/signature/state/nonce/PKCE/callback/return-path tests;
- parameter, query/body boundary, SQL-injection, stored/reflected XSS, and
  sanitizer corpus tests;
- request/publication limiter bypass, restart, capacity, proxy-spoofing, and
  adapted-Caddy overwrite tests; and
- bounded application log review for tokens, cookies, credentials, identities,
  addresses, restricted content, queries, drafts, rules, and target URLs.

The deployed Beta record states the configured Authentik revalidation interval.
An isolated designated identity is disabled at the provider and may retain only
the already admitted public anonymous surface until the next protected request
at or after that interval enters reauthorization and fails. The old local
session cannot regain authority. Separate database tests prove local role,
group, suspension, mute, and target-session revocation on the next protected
request. No evidence may claim immediate Authentik disable propagation.

### 23.3 Accessibility, usability, and representative-data matrix

Core browser journeys are:

1. visitor navigation, rules, search, activity, and restricted denial;
2. member login, topic creation/preview, parent-addressed reply, edit,
   soft-delete, unread navigation/mark-read, and report submission;
3. matching/nonmatching group access transitions;
4. moderator report assignment/note/resolution and topic/post/account actions;
5. administrator settings, accounts, roles, groups, memberships, areas, and
   dashboard; and
6. logout, stale session revalidation, validation/conflict, `422`, `429`,
   capacity `503`, and unexpected request failure recovery.

Each journey runs through Caddy at empty and `/bb` base paths where the test
harness controls configuration. Ordinary HTML is exercised with JavaScript
disabled; HTMX-enabled repetitions prove history, focus, status/error
announcements, draft preservation, and no duplicated shell/footer. Automated
accessibility results are followed by retained manual keyboard evidence for
tab order, skip/navigation landmarks, heading structure, accessible names,
labels/descriptions/errors, visible focus, no trap, and control operability.
Record 320 CSS pixel reflow, 200% zoom, mobile and desktop viewports, contrast,
non-color state cues, browser/version, screenshots or accessibility-tree
snapshots where useful, and every manual gap.

The representative PostgreSQL 17 population retains at least 25,000 accounts,
100,000 topics, 1,000,000 ordinary posts, the admitted depth-32 topic, current
report/admin/search/unread fixtures, and 1,000-account publication contention.
For index, area list, topic, direct post, search, activity, unread, report queue,
moderation, and administration pages, record query count, rows, buffers,
custom/generic plan structure, elapsed time, allocations where measured, pool
peak/return-to-baseline, cancellation, and bounded continuation edges. A mixed
wave runs authentication, ordinary reads, discovery, unread, publication,
moderation, administration, and backup observation without accepting a partial
result or leaked private state. These are observations, not a latency or
multi-replica claim.

### 23.4 Alpha.2 upgrade and initial recovery matrix

Before touching the live Alpha.2 database, capture without secrets:

- exact application commit/version/image and Compose/Caddy identities;
- PostgreSQL image/version, database identity, migration head, relation sizes,
  runtime/migration roles, and grants;
- durable mount identity and free-space check;
- configuration key inventory and digests of non-secret deployment artifacts;
- active readiness/smoke state; and
- exact rollback artifact, configuration, and pre-upgrade backup destination.

The first rehearsal restores a digested copy of the live Alpha.2 database into
a clean task-owned PostgreSQL 17 instance. With the application stopped for the
database under test, run migrations 000006 through 000011, renderer and search
completion, packaged runtime grants, exact-head readiness, and the complete
application smoke matrix. Record locks, scans, I/O, elapsed time, schema heads,
row/count continuity, audits, private visibility, and failed/unknown-outcome
procedures. The live release repeats the same stopped sequence only after this
rehearsal passes and a fresh exact pre-upgrade backup verifies.

Initial recovery creates a second digested logical backup of the Beta schema
plus a non-secret configuration/release inventory, restores it into another
clean PostgreSQL 17 instance, reapplies only packaged runtime grants, and runs
readiness plus smoke using the exact candidate artifact. Record backup and
restore durations, sizes, digests, recovery point, recovery time, cleanup, and
the honest storage failure domain. Beta may publish same-host-only storage,
missing scheduling/retention/encryption/alerting, and lack of production RPO/RTO
as limitations; stable cannot inherit those claims without its later gate.

No rehearsal runs `docker compose down -v`, deletes or rewrites the live data
mount, puts database URLs in process arguments/evidence, or treats a destructive
down migration as rollback.

### 23.5 Integrated admission, release, and confirmation

On one exact final candidate, run deterministic SQL/Templ/static generation,
gofmt, vet, repository-wide unit race/relevant coverage, full PostgreSQL 17
integration/race, the complete Beta requirement/route/leakage/security,
accessibility/browser, representative-data/resource, upgrade/recovery,
package/image/SBOM, log-redaction, and repository-integrity gates. Produce two
byte-identical Linux/amd64 packages. Evidence records exact commit/tree,
commands, environment/tool versions, timestamps, digests, results, and gaps.

Two fresh independent cold reviews must both say CLEAN on that exact state.
Only then may the guarded PR fast-forward to `main` and mirror. The annotated
`1.0.0-beta.1` tag is created only on the merged commit; the release package
and image are rebuilt or verified from that tag. Deployment preserves the
PostgreSQL container/data mount, replaces only the application after the
stopped migration/grant step, and runs the complete smoke and rollback checks
through Caddy for designated test users.

The release record publishes known limitations and retains the prior artifact,
pre-upgrade backup, failed-step recovery decision, tag, commit, tree, package,
image, schema, and configuration identities. The candidate becomes known-good
only after the owner confirms the real browser workflow. Beta.1 completion
stops before RC.1; no migration or artifact freeze is implied.

### 23.6 Standalone-stack corrective admission

The Beta.1 standalone successor proves one six-service Compose project with
content-pinned Caddy, Board, PostgreSQL 17, Authentik server/worker, and
Authentik PostgreSQL 16 images. Static and rendered checks reject mutable
images, Docker-socket access, shared durable paths, unexpected listeners,
secret-bearing interpolation, ambiguous proxy chains, and any application or
provider outside the exact Board blueprint.

A clean-host rehearsal applies the blueprint twice, migrates Board through
000012, provisions one dedicated Authentik UUID subject, completes a real
Chromium OIDC authorization/callback/session journey, proves an Authentik
outage creates no session, and restarts all six services without identity or
data drift. The identity-rebind gate proves exact old/new issuer and subject
selection, complete session and pending-attempt revocation, one audit row, and
unchanged user, role, group, ownership, and content state.

Both databases receive separate logical backups and clean matching-major
restores. The restored Board receives the packaged runtime grants before a
smoke test at the unchanged Board/Auth origins proves identity continuity.
Caddy/Auth non-database state and required secret references are inventoried
separately. Direct-host evidence proves stack Caddy derives the
client from its peer. Shared-host evidence proves the retained TLS edge
overwrites a single canonical address and stack Caddy is loopback-only, trusts
only that value, removes alternate identity headers, supplies the exact public
HTTPS scheme upstream, and leaves unrelated host routes unchanged. Final live
HTTPS browser, restart, backup, rollback-readiness,
artifact-custody, two-CLEAN-review, guarded-delivery, and owner-confirmation
rules remain mandatory.

## 24. B1-09 Board administration control-plane evidence contract

### 24.1 Schema, settings, and maintenance

- Fresh and 000012-upgrade databases reach exact head 000013 with registration
  closed, maintenance off, packaged policy values, positive revisions, exact
  checks/defaults/indexes/foreign keys, and no rewritten content or identity.
- Catalog and restricted-role tests prove the exact grant delta and reject
  settings insert/delete/key update, pending/email-state deletion, token-hash
  reads, Authentik database access, and every unlisted mutation.
- Control mutation tests cover every closed value, malformed/duplicate field,
  UTF-8/control/size edge, startup ceiling, cross-field rule, stale/no-op/
  overflow revision, actor revocation, timeout, rollback, audit failure, and
  unknown commit. Each successful request has one settings change and one
  redacted audit.
- Publication tests prove new database policy is used in the existing locked
  counter transaction under concurrent old/new-account requests, restart, and
  a simultaneous policy change. The immutable request limiter and blocked-link
  policy do not drift.
- Session authentication proves tightened idle/revalidation values apply on the
  next protected request and can never exceed maximum-age/startup ceilings.
- Maintenance tests cover visitor/member/moderator/administrator, every public
  and protected route class, static/health/readiness, login/callback/
  revalidation/logout/setup, registration denial, HTMX/full/no-script, and
  administrator recovery. No response leaks private state or changes the
  process/container lifecycle.

### 24.2 Authentik admission and permission matrix

A disposable pinned Authentik 2026.5.2 instance applies the blueprint twice.
The gate records exact object identities and proves three flows/groups, one
service account/role/token, no unrelated application/provider, and no drift on
second apply. For each open, approval, invitation, closed, maintenance, Board
outage, database outage, timeout, redirect, non-204, and oversized-response
case, both `/register` and the direct `/if/flow/.../` URL produce the same
admission result. Expression requests use fixed method/URL/timeout and emit no
credential or profile data. A mode change between flow entry and the first
irreversible stage is denied by the second fresh policy evaluation.

The service-token matrix must positively prove only:

- one exact user lookup and exact group read;
- add/remove user on accepted, pending, and suspended groups;
- create/read/delete one invitation bound to the invitation flow.

It must receive `403` or equivalent denial for admin-interface access,
user create/change/delete/password/recovery, arbitrary group create/change/
delete, membership on a non-Board group, flow/stage/policy/application/provider/
role/token/secret mutation, invitation `send_email`, task list/status/retry,
event/log export, and impersonation.
The control token, invitation UUID/link, email, user UUID/numeric key, and
remote bodies are absent from retained commands, logs, screenshots, and Git.

### 24.3 Registration, approval, invitation, and reconciliation

- Signed-intake tests cover algorithm/key, issuer, audience, purpose, flow,
  `jti`, issued/expiry bounds, numeric ID, UUID, body/content-type/query,
  UTF-8/profile limits, replay, terminal replay, concurrent first intake, and database
  failure. Invalid cases perform no pending write and return the fixed class.
- Real-browser journeys prove open enrollment verifies email and yields only a
  local member; approval enrollment verifies into pending with no OIDC Board
  access until approval; rejection never grants access; invitation mode rejects
  missing/wrong/expired/reused/cross-flow tokens and accepts one valid
  single-use invitation.
- Approval/rejection tests inject failure before/after every Authentik call,
  membership readback, Board state transition, audit, and commit. Concurrent
  approve/reject/retry requests converge to one terminal state without granting
  a rejected identity. Lock observation proves no PostgreSQL transaction spans
  an Authentik request. OIDC callback tests prove every non-approved local
  pending state denies session creation even while the accepted group is
  deliberately permissive.
- Pending-orphan tests lose the signed intake after Authentik commits, retain
  local rows during Authentik outage, bound the remote section and overflow,
  reject forged/stale/cross-group handles, and adopt exactly one re-fetched
  pending-only identity without granting access.
- Suspension tests prove local denial and all-session revocation commit before
  Authentik removal; an Authentik outage cannot restore Board access.
  Reinstatement cannot clear local denial until accepted-group membership is
  verified. Reconciliation is idempotent and operates on only one identity.
- Invitation tests cover email/name/expiry bounds, exact flow/single-use/fixed
  read-only email data, prompt-before-consume ordering, Authentik 2026.5.2's
  consumption-at-stage behavior, 51-row continuation, one-time link display,
  signed revoke handles, local-intent/remote/completion failures, exact-name
  adoption using the retry's validated form values, remote mismatch, Board SMTP
  queued/failure/unknown results, no send while adopting an existing object,
  no resend after ambiguity, revocation, audit
  redaction, no PostgreSQL lock across HTTP, and no arbitrary-recipient
  test-mail path.

### 24.4 Sessions, email, UI, and integrated delivery

- Session queries prove administrator authorization occurs before target/session
  relations, return at most 51 rows, and never select or render token hash,
  cookie, IP prefix, or user-agent hash. Handle tamper/expiry/cross-actor/
  cross-user/action tests fail before mutation. One/all revocation covers zero,
  current-session cookie clearing, concurrency, audit failure, and unknown
  commit.
- Email tests cover disabled/malformed configuration, STARTTLS and implicit TLS
  with hostname verification, authentication failure, timeout before DATA,
  disconnect after DATA, accepted result, per-admin rate/idempotency, absent
  verified address, and fixed self-addressed content. Board persists/logs no
  recipient, SMTP transcript, or message body. Board never requests Authentik
  task or event data; disposable-stack and deployment smoke prove the actual
  Authentik enrollment-email worker path outside the browser control plane.
- Every added page passes exact route/query/body/CSRF/revalidation/cache/status
  tests, ordinary HTML and HTMX parity, base paths empty and `/bb`, JavaScript
  absent, keyboard order/focus/status/error semantics, automated accessibility,
  320-pixel reflow, 200% zoom, and bounded allocation/query/pool behavior.
- Exact-candidate gates include deterministic generation, format, vet, full and
  race suites on the development host, relevant coverage with explicit gaps,
  PostgreSQL 17.10, Authentik 2026.5.2, SMTP, Caddy/Chromium, permission
  negatives, logs/secrets, package/SBOM/reproducibility, and repository
  integrity. Separate Board/Auth backups restore cleanly and the restored stack
  repeats mode, approval, session, maintenance, email, OIDC, restart, rollback,
  and forward-recovery smoke.

Two fresh independent cold reviews must both return CLEAN on the exact final
commit/tree. Guarded merge/mirror, annotated successor Beta tag, package/image,
live deployment, smoke, rollback proof, and owner physical acceptance remain
separate explicit gates. B1-09 cannot bypass unresolved Beta.1.5 acceptance and
does not admit B1-10 or RC.1.
