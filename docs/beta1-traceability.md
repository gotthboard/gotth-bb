# Beta.1 requirement traceability

This is the exact version 1.0 implementation inventory used by B1-00 and the
Beta.1 admission gate. `implemented` means a concrete implementation and
existing focused evidence exist; it does not claim that the final exact-tree
Beta gate has run. `blocked` names work that must close before Beta.1 may be
released. No requirement is declared inapplicable.

The verification gate parses the requirement identifiers in `prd.md` and this
table and rejects omission, duplication, or invention in either direction.

| Requirement | Implementation | Existing evidence | Beta state |
| --- | --- | --- | --- |
| `ID-001` | `internal/auth`, OIDC HTTP composition | `internal/auth/*test.go`, `internal/httpui/*login*test.go` | implemented |
| `ID-002` | `external_identities` schema and identity lookup | `migrations/schema_integration_test.go`, `internal/auth/*integration_test.go` | implemented |
| `ID-003` | OIDC callback transaction and user upsert | `internal/auth/initial_session_integration_test.go` | implemented |
| `ID-004` | approved profile-claim refresh in callback transaction | `internal/auth/initial_session_integration_test.go` | implemented |
| `ID-005` | local role/group tables and policy context | AN-04 evidence and `internal/administration/*integration_test.go` | implemented |
| `ID-006` | hashed server sessions, rotation, expiry, logout | `internal/auth/*session*test.go`, HTTP session tests | implemented |
| `ID-007` | database-current suspension checks | AN-01/AN-04 evidence and session integration tests | implemented |
| `ID-008` | configured OIDC revalidation boundary | B1-01 exact 30-minute config and local revalidation evidence; live provider-disable proof pending B1-05 | blocked |
| `ID-009` | OIDC-only authentication; no local credential store | schema/config/source inventory and secret scans | implemented |
| `ID-010` | exact-subject audited first-administrator claim | governance and operator command integration tests | implemented |
| `ID-011` | controlled Authentik enrollment redirect | registration handler/config tests and deployment blueprint validation | implemented |
| `ID-012` | fail-closed registration activation gate | registration handler tests and deployed `REGISTRATION_ENABLED=false` state | implemented |
| `ID-013` | independent setup and registration routes/state | setup/registration HTTP tests | implemented |
| `ID-014` | B1-09 durable four-mode policy and direct Authentik flow admission | `docs/evidence/beta1-09-01-control-settings.txt`; B1-09-03 HTTP/Auth evidence pending | blocked |
| `ID-015` | B1-09 verified pending approval and flow-bound invitation lifecycle | B1-09-02/03 real Authentik and transition evidence | blocked |
| `ID-016` | B1-09 restrictive local/Auth group suspension reconciliation | B1-09-03 failure/retry and live OIDC evidence | blocked |
| `ACL-001` | closed visibility/posting policy types and constraints | policy tests and migration schema tests | implemented |
| `ACL-002` | area-owned authorization predicates in topic/post paths | database access and HTTP leakage tests | implemented |
| `ACL-003` | no topic-level visibility model | schema and route inventory | implemented |
| `ACL-004` | server-side policy before full/HTMX data use | access, handler, and browser evidence through AN-05 | implemented |
| `ACL-005` | authorization-first discovery/unread/count projections | AN-02, AN-03, and AN-04 integrated evidence | implemented |
| `ACL-006` | missing/unauthorized direct-object equivalence | database and HTTP leakage tests | implemented |
| `ACL-007` | audited area/group/posting transitions | AN-04 evidence | implemented |
| `ACL-008` | explicit moderator/administrator elevated reads | policy/store/moderation tests | implemented |
| `FORUM-001` | one-level `areas` model | migrations and area store/HTTP tests | implemented |
| `FORUM-002` | area fields, ordering, modes, visibility | Alpha.2/AN-04 tests and evidence | implemented |
| `FORUM-003` | topic and parent-addressed reply publication | forum/publishing integration and browser tests | implemented |
| `FORUM-004` | rooted bounded reply tree | Alpha.2 schema/store/browser tests | implemented |
| `FORUM-005` | topic/post moderation transitions | AN-01 evidence and moderation integration tests | implemented |
| `FORUM-006` | stable identifiers, canonical URLs, direct post | URL, topic, and AN-02 direct-post tests | implemented |
| `CONTENT-001` | Markdown authoring, preview, sanitized persistence | Alpha.3 integrated evidence | implemented |
| `CONTENT-002` | pinned GFM renderer | Alpha.3 integrated evidence | implemented |
| `CONTENT-003` | raw HTML disabled and exact sanitizer policy | renderer/XSS tests and Alpha.3 evidence | implemented |
| `CONTENT-004` | author edit with visible timestamps/revisions | editing service/HTTP tests | implemented |
| `CONTENT-005` | soft deletion and tombstone behavior | forum/editing/moderation tests | implemented |
| `CONTENT-006` | bounded validation and draft preservation | publishing/editing HTTP/browser tests | implemented |
| `CONTENT-007` | excluded V1 rich-content surfaces absent | dependency, route, schema, and template inventory | implemented |
| `READ-001` | index, area list, threaded topic, direct post | Alpha.2/AN-02/AN-03 browser and store evidence | implemented |
| `READ-002` | unread models, first-unread, mark-read | AN-03 integrated evidence | implemented |
| `READ-003` | bounded PostgreSQL search/recent activity | AN-02 integrated evidence | implemented |
| `READ-004` | authorization-first discovery projections | AN-02 plan/leakage evidence | implemented |
| `READ-005` | base-path URL builder and canonical navigation | URL/HTTP/browser tests at empty and `/bb` paths | implemented |
| `READ-006` | bounded tree pagination and reply context | Alpha.2/AN-03 population evidence | implemented |
| `MOD-001` | member report creation | AN-01 report service/HTTP tests | implemented |
| `MOD-002` | paged moderation queue and processing | AN-01 integrated evidence | implemented |
| `MOD-003` | complete topic/post transition set | AN-01 integrated evidence | implemented |
| `MOD-004` | warn/mute/suspend/reinstate | AN-01/AN-04 evidence | implemented |
| `MOD-005` | request and durable publication limits | AN-05 integrated evidence | implemented |
| `MOD-006` | blocked domain/exact-URL policy | AN-05 integrated evidence | implemented |
| `MOD-007` | atomic moderation/administration audit writes | AN-01/AN-04 integration evidence | implemented |
| `MOD-008` | no application audit-edit surface | schema grants and route inventory | implemented |
| `ADMIN-001` | bounded audited area administration | AN-04 integrated evidence | implemented |
| `ADMIN-002` | private account paging/status management | AN-04 integrated evidence | implemented |
| `ADMIN-003` | local roles/groups/memberships | AN-04 integrated evidence | implemented |
| `ADMIN-004` | singleton presentation/rules settings | AN-04 and AN-05 settings-policy evidence | implemented |
| `ADMIN-005` | exact administrator dashboard counts | AN-04 population/plan evidence | implemented |
| `ADMIN-006` | B1-09 registration, maintenance, publication, and session policy controls | `docs/evidence/beta1-09-01-control-settings.txt`; B1-09-04 administrator UI/browser evidence pending | blocked |
| `ADMIN-007` | B1-09 pending-registration and invitation administration | B1-09-03 Authentik/HTTP/browser evidence | blocked |
| `ADMIN-008` | B1-09 bounded local-session views and revocation | B1-09-04 database/HTTP/security evidence | blocked |
| `ADMIN-009` | B1-09 email capability, bounded test status, and self-addressed test | B1-09-04 SMTP/HTTP evidence | blocked |
| `UX-001` | responsive server-rendered surface | B1-02 exact 320-pixel/200%-zoom Caddy/Chromium evidence | implemented |
| `UX-002` | semantic keyboard-operable core flows | B1-02 native keyboard/no-script and accessibility-tree evidence | implemented |
| `UX-003` | labels, descriptions, errors, status regions | B1-02 Axe, accessibility-tree, error/status, and manual contrast evidence | implemented |
| `UX-004` | ordinary HTML with HTMX-equivalent enhancement | full/HTMX/no-script tests through AN-05 | implemented |
| `UX-005` | diagnosable failed HTMX behavior | B1-02 failure/draft/focus/history plus focused handler/browser evidence | implemented |
| `UX-006` | immutable GOTTH Board footer attribution | B1-08F exact-label/target, tenant-name negative control, fragment, package, and deployed HTTPS evidence | blocked |
| `UX-007` | responsive visible Area administration controls | B1-08G rendered markup, 320px/desktop browser geometry, focus, package, and deployed HTTPS evidence | blocked |
| `SEC-001` | CSRF validation on unsafe routes | CSRF middleware and every mutation's HTTP tests | implemented |
| `SEC-002` | scoped Secure/HttpOnly/SameSite cookies | B1-01 exact cookie construction and two-base-path Caddy inspection; live deployment smoke remains B1-05 | implemented |
| `SEC-003` | CSP and defensive browser headers | B1-01 exact Caddy/browser header evidence at both base paths | implemented |
| `SEC-004` | escaped templates and boundary sanitizer | B1-01 fixed-parser/raw-tag regression plus template/render/XSS tests | implemented |
| `SEC-005` | bounded structured redacted logs | observability and AN-02/AN-05 log evidence | implemented |
| `OPS-001` | ordered attested PostgreSQL migrations | migration/readiness tests through 000013 and `docs/evidence/beta1-09-01-control-settings.txt` | implemented |
| `OPS-002` | bounded public liveness/readiness | readiness and deployed health tests | implemented |
| `OPS-003` | structured request-correlated logs | observability tests and deployed journald configuration | implemented |
| `OPS-004` | deployment/migration/backup/restore/rollback procedures | B1-03 packaged-helper, live-copy upgrade, Beta backup/clean-restore, readiness, and rollback rehearsal complete; live release record pending B1-05 | blocked |
| `OPS-005` | fail-closed identity/session/access validation | auth/policy/readiness/failure tests | implemented |
| `OPS-006` | standalone pinned Caddy/Auth/Board stack with separate databases | B1-08 exact candidate Compose, identity-cutover, recovery, isolation, restart, OIDC, and outage evidence complete; guarded delivery and live shared-host deployment evidence pending | blocked |

## Existing retained evidence

- Alpha.3: `docs/evidence/alpha3-dense-01c0247.txt` and
  `docs/evidence/alpha3-population-01c0247.txt`.
- AN-02: `docs/evidence/an02-04-admission-1805fb6.txt`, with its linked
  browser, integration/race, release, and verification records.
- AN-03: `docs/evidence/an03-04-integrated-51e3366.txt`.
- AN-04: `docs/evidence/an04-04-integrated-1d1e100.txt`.
- AN-05: `docs/evidence/an05-04-integrated-1abbeab.txt`.
- B1-01: `docs/evidence/beta1-01-security-f91a6fb.txt`.
- B1-08 candidate: `docs/evidence/beta1-08-standalone-stack-eef7c48.txt`.
- B1-09 contract: admitted from commit
  `a1ff1eaef5aa16d18073fa5281cc16a3cea41d3d` / tree
  `0cc2556b3e7b0c9b64b6bb63265635a69c5b1c61` after two fresh CLEAN reviews;
  implementation evidence remains pending.

The final Beta evidence replaces pending references with exact commit/tree,
command, environment, result, and gap records. RC.1 still owns final evidence
completeness and artifact/migration freeze.
