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
| ACL-001–ACL-003 | area schema and policy types | UT, DB, REV | Alpha.1 |
| ACL-004–ACL-006 | every area-owned query and route | DB, HTTP, E2E, SEC | Alpha.1 |
| ACL-007–ACL-008 | admin/moderation transactions and audit | DB, HTTP, REV | Alpha.1 |
| FORUM-001–FORUM-002 | area schema, stores, and pages | UT, DB, HTTP, E2E | Alpha.1 |
| FORUM-003–FORUM-004 | parent-addressed publishing and threaded topic pages | UT, DB, HTTP, E2E | Alpha.2 |
| FORUM-005–FORUM-006 | moderation state and stable URLs | UT, DB, HTTP, E2E | Alpha.1 |
| CONTENT-001–CONTENT-007 | composer, renderer, post services | UT, DB, HTTP, SEC | Alpha.1 |
| READ-001 | index/list/threaded-topic handlers | HTTP, E2E, A11Y | Alpha.1 baseline; Alpha.2 threaded |
| READ-002 | read markers and unread views | UT, DB, HTTP | Beta.1 |
| READ-003–READ-004 | PostgreSQL search/activity queries | DB, HTTP, SEC, E2E, PERF | Alpha.N (AN-02) |
| READ-005 | URL builder and templates | UT, HTTP, E2E | Alpha.1 |
| READ-006 | tree-order paging and reply-to context | UT, DB, HTTP, E2E, A11Y | Alpha.2 |
| MOD-001–MOD-002 | report service and queue | UT, DB, HTTP, E2E | Beta.1 |
| MOD-003–MOD-004 | moderation transitions | UT, DB, HTTP, E2E | Alpha.1 minimum; Beta complete |
| MOD-005–MOD-006 | abuse controls | UT, HTTP, SEC, OPS | Beta.1 |
| MOD-007–MOD-008 | audit transaction/store | DB, HTTP, REV | Alpha.1 |
| ADMIN-001–ADMIN-003 | area/account/group administration | UT, DB, HTTP, E2E | Alpha.1 minimum; Beta complete |
| ADMIN-004–ADMIN-005 | settings and authorized counts | DB, HTTP, E2E | Beta.1 |
| UX-001–UX-005 | Templ/HTMX/Tailwind UI | HTTP, E2E, A11Y | Beta.1 |
| SEC-001–SEC-005 | middleware, rendering, logging, config | UT, HTTP, SEC, REV | Alpha.1, repeated at RC |
| OPS-001–OPS-005 | migrations, health, logs, deployment | DB, OPS, REV | Alpha.1 minimum; Stable complete |

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
`aff08252cbe283ecd9365f85b78c8a2c44a8b064`, tree
`fb2e8f0b0374b740f4f288f4f473be034393fc7c`, populated 25,000 topics and
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
[`docs/evidence/an02-02-plans-aff0825.txt`](evidence/an02-02-plans-aff0825.txt),
SHA-256
`1aed451c29ab2132a7ce75194c6a252f59ca0daa5ab19ca931219684e50c4b16`.

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
