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
| FORUM-001–FORUM-006 | area/topic/post stores and pages | UT, DB, HTTP, E2E | Alpha.1 |
| CONTENT-001–CONTENT-007 | composer, renderer, post services | UT, DB, HTTP, SEC | Alpha.1 |
| READ-001 | index/list/topic handlers | HTTP, E2E, A11Y | Alpha.1 |
| READ-002 | read markers and unread views | UT, DB, HTTP | Beta.1 |
| READ-003–READ-004 | PostgreSQL search/activity queries | DB, HTTP, SEC | Beta.1 |
| READ-005 | URL builder and templates | UT, HTTP, E2E | Alpha.1 |
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
