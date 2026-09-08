# Release and operations plan

## Document control

| Field | Value |
| --- | --- |
| Status | Active alpha deployment; container runtime selected |
| Initial development base | `https://bb.alhstudios.com/` |
| Target host | `development` (`10.0.0.97`) |
| Service manager | Docker Engine 29.7.1 / Docker Compose 5.4.0; Docker daemon supervised by systemd |
| Identity provider | Authentik OIDC |
| Edge proxy | Caddy |
| Durable store | PostgreSQL 17.10 in a separate container with host-bound durable data |
| Verification contract | [Traceability and verification](verification.md) |

## 1. Operational principles

- Release artifacts are immutable and traceable to one commit.
- Configuration and secrets are external to the artifact and repository.
- Database migration is a separate, visible release step.
- Readiness is enabled only after configuration, schema compatibility, and the
  required singleton governance row pass validation.
- Rollback is planned before deployment.
- Unknown commit outcomes are investigated, not blindly retried.
- Backups are not considered valid until restoration succeeds.
- Restricted content and credentials do not enter logs, metrics, or public
  health output.
- First-administrator setup is deployed with `REGISTRATION_ENABLED=false`.
  Public registration is enabled only after the one-time setup succeeds, the
  Authentik enrollment blueprint and email delivery are verified, and every
  sibling Authentik application has an explicit access binding that excludes
  the board-only group.

## 2. Environments

The ALH Studios URL is a temporary development deployment target. GOTTH Board
is not coupled to that domain; each deployment supplies its own validated
public base URL and path.

### 2.1 Local development

- Local PostgreSQL with disposable data.
- Controlled test OIDC issuer or dedicated Authentik development application.
- `BASE_PATH` exercised as both empty and `/bb`.
- No production secrets or copied production database.

### 2.2 Automated test

- Ephemeral PostgreSQL at a supported version.
- Deterministic fixtures for the complete access matrix.
- Controlled OIDC endpoints and signing keys.
- Fresh and upgrade migration jobs.

### 2.3 Alpha/beta

- Real Caddy site for `bb.alhstudios.com` with an empty base path.
- Dedicated Authentik application/client.
- Dedicated PostgreSQL database and credentials.
- Access restricted to designated test users/groups until the owner approves
  public testing.
- Production-like logging, backup, migration, and rollback mechanisms.

### 2.4 Production

Production is established only after `1.0.0-rc.N` passes the stable release
gates. Reusing the beta host is allowed only after its data, secrets, backups,
monitoring, and recovery posture are explicitly accepted.

## 3. Release identity

Releases follow semantic versioning:

- `1.0.0-alpha.N`: integrated but incomplete version 1.0.
- `1.0.0-beta.N`: version 1.0 feature-complete user testing.
- `1.0.0-rc.N`: release candidate with no known scope gaps.
- `1.0.0`: stable.
- `1.0.N`: compatible bug/security corrections.
- `1.N.0`: compatible feature releases in the 1.x line.
- `N.0.0`: major capability or intentionally incompatible contract.

Build metadata may include date and abbreviated commit, but precedence and
deployment decisions use the base semantic version and artifact digest.

Release builds inject the exact version and full lowercase 40-character Git
commit into the package-private `internal/buildinfo.version` and
`internal/buildinfo.commit` linker targets with
Go linker `-X` flags. The pair is accepted together or rejected together;
ordinary developer builds report the explicit `development`/`unknown`
sentinels. The forum validates the pair before binding its listener and writes
it in the structured `service starting` record. The matching migration and
operator binaries report the same database-free identity with:

```sh
gotth-bb-migrate version
gotth-bb-operator version
```

An artifact whose migration/operator identities differ from its release record
or whose forum startup identity differs from those outputs is not deployable.

Each deployed release record contains:

- Version and Git commit.
- Artifact digest.
- Build toolchain and dependency lock state.
- Migration head before and after deploy.
- Configuration schema version.
- Deployment timestamp and operator.
- Verification result and evidence links.
- Rollback target.

## 4. Build artifact

The release pipeline produces:

- Go service binary.
- Migration command or verified migration subcommand.
- Compiled Templ output as part of the binary/build.
- Pinned HTMX and compiled/versioned Tailwind/static assets.
- Software bill of materials or dependency manifest.
- Checksums/digests.
- The validated semantic version is shown in the public page footer. The full
  commit remains available through the database-free migration/operator
  `version` commands and structured forum startup log; it is not exposed by a
  public commit endpoint.

The repository's `make release` target requires explicit `RELEASE_VERSION`,
`RELEASE_COMMIT`, `RELEASE_GOOS`, `RELEASE_GOARCH`, and `RELEASE_OUTPUT`
environment values. It runs `make verify` first. The packaging command then:

1. Rejects an invalid release identity, foreign platform, existing output,
   dirty worktree, or commit that differs from `HEAD`.
2. Disables workspace discovery, per-user Go configuration, ambient build
   flags, CGO, toolchain switching, and FIPS source substitution; the native
   architecture is built at its documented baseline.
3. Builds `gotth-bb`, `gotth-bb-migrate`, and `gotth-bb-operator` with
   `-trimpath`, VCS stamping disabled, and one exact linker identity.
4. Executes the built migration and operator binaries' `version` commands and
   requires the exact requested version/commit result from both before
   packaging.
5. Writes a lexically ordered tar archive with fixed modes, zero owner/group,
   and no gzip timestamp, then emits the archive digest in `SHA256SUMS`.
6. Rechecks the exact `HEAD` and clean worktree after all builds, then renames a
   private staging directory into the requested output only after identity
   validation, archive writing, and checksum writing succeed.

Archive contents are rooted at
`gotth-bb-VERSION-GOOS-GOARCH/` and include the three binaries,
`DEPENDENCIES.txt`, `RELEASE.txt`, the runtime PostgreSQL grant, and the exact
container build/entrypoint/Compose contracts. The release metadata contains no
build timestamp or host path. The same clean commit built twice with the same
pinned toolchain and platform must produce byte-identical archive and checksum
files.

Builds run from a clean checkout. A dirty worktree, generated-code drift, test
failure, or secret finding blocks artifact publication.

### 4.1 Application container image

The application image is built only from an extracted, checksum-verified
release archive. Its runtime base is
`alpine@sha256:25109184c71bdad752c8312a8623239686a9a2071e8825f20acb8f2198c3f659`
(Alpine 3.23.3). The build supplies the validated version and full commit as
OCI labels and does not compile source. The exact image ID and RepoDigest, when
published, join the deployment record; a mutable tag alone is not identity.

The image contains no deployment environment or secret. The forum runs as
UID/GID 65532. Docker health executes the image's loopback liveness probe. The
application service uses host networking and still binds only
`127.0.0.1:18082`; this preserves the production configuration rule and the
same host-local reachability as the prior native process. It is not a claim of
container network isolation.

## 5. Caddy contract

The intended route shape is:

```caddyfile
bb.alhstudios.com {
    reverse_proxy 127.0.0.1:18082
}
```

The final Caddy configuration must be merged with the existing site rather than
blindly replacing the site block. Before reload:

1. Resolve the current canonical Caddyfile and active configuration.
2. Confirm that `bb.alhstudios.com` does not collide with another site block.
3. Confirm the application listen address and firewall boundary.
4. Format and validate the complete configuration.
5. Capture the prior configuration for rollback.
6. Reload rather than terminate active traffic.
7. Verify `/`, assets, health routing policy, and an unknown path.

The application uses configured `PUBLIC_BASE_URL` and `BASE_PATH`; it does not
trust incoming host or prefix headers to generate callbacks or links.

## 6. Authentik contract

The alpha environment requires a dedicated OIDC provider/application with:

- Exact redirect URI `https://bb.alhstudios.com/auth/callback`.
- Exact post-logout return URI if RP-initiated logout is enabled.
- Authorization Code flow.
- Confidential client credentials stored outside Git.
- Required stable identity and approved profile claims only.
- Test identities whose forum roles and local groups are assigned in GOTTH
  Board, not by Authentik claims.

Before deployment, record without secrets:

- Issuer URL.
- Client/application identifier.
- Approved claim names and expected types.
- Issuer/subject pair selected for the explicit first-administrator grant.
- Session and token lifetimes relevant to revocation behavior.

Client secrets and tokens are never placed in issue bodies, CI logs, release
notes, screenshots, or repository files.

## 7. PostgreSQL contract

- Alpha supports PostgreSQL 17; integration evidence is pinned to PostgreSQL
  17.10 (`postgres@sha256:a426e44bac0b759c95894d68e1a0ac03ecc20b619f498a91aae373bf06d8508d`).
- The forum uses a dedicated database role with only required privileges.
- Migration privileges are separated from runtime privileges where practical.
- After migrations, the migration owner applies
  `deploy/postgresql/runtime-grants.sql` with the exact runtime role as psql's
  `runtime_role` variable. The idempotent artifact grants only
  `UPDATE(singleton)` on `governance_state`, which PostgreSQL requires for
  `SELECT ... FOR UPDATE`, plus `SELECT` on the migration-owned
  `content_renderer_state` readiness singleton. Table-wide governance UPDATE,
  UPDATE on `created_at`, renderer-state mutation, and DELETE remain denied.
- After 000008, the same packaged grant artifact adds only `SELECT` on the
  migration-owned `search_projection_state` singleton for runtime readiness;
  runtime never owns or mutates that table.
- Migration 000009 adds no singleton, secret, or custom runner. Its regular
  partial index, including `author_id`, scans `posts` and can block writers; its
  finite-time constraint validation scans `topic_reads`. Both are measured
  maintenance work even though no row backfill or rewrite occurs. A legacy
  nonfinite `read_at` aborts the entire migration and leaves the ledger at
  000008; inspect and apply a
  reviewed forward repair before retrying rather than deleting or inventing
  marker state.
- Migration 000010 adds the site-settings singleton, positive administration
  revisions, and revised audit checks. PostgreSQL's constant defaults avoid
  rewriting existing user/group/area rows, but constraint validation and audit-
  check replacement still take measured table locks and scans. The packaged
  runtime grants add only the SELECT/INSERT/UPDATE privileges required by the
  admitted operations and no DELETE on settings, users, groups, areas, or
  mapping rows.
- Connections require the deployment's approved transport protection.
- Pool sizes and timeouts are bounded and fit the server connection budget.
- PostgreSQL version support is documented and tested.
- Before a PostgreSQL 17 minor update, AN-02's stopped preflight recomputes
  every stored search vector on the candidate server. Any byte difference
  requires a new projection identity and complete rebuild before service.
- Database access is not public.
- The alpha Compose service preserves the pinned PostgreSQL container image,
  loopback-only maintenance port, and
  `/tank/gotth-bb/postgres17:/var/lib/postgresql/data` bind mount. Application
  container replacement does not recreate or migrate that data directory.
- The host-networked application connects through PostgreSQL's existing
  `127.0.0.1:55435` maintenance publication. It does not require a database
  container restart or network reattachment.

Migration state is checked before readiness. An application that expects a
different schema head fails closed with an operator-visible error.

### 7.1 First-administrator operator command

The selected administrator must first complete Authentik login so the exact
issuer/subject identity already exists locally. With `DATABASE_URL` supplied by
the approved secret mechanism, an authorized operator runs exactly once:

```sh
go run -mod=readonly ./cmd/operator bootstrap-administrator \
  --issuer 'exact-validated-issuer' \
  --subject 'exact-provider-subject' \
  --operator 'operator-audit-identifier'
```

The arguments are identity/audit data, not forum-role claims from Authentik.
The command prints only committed user and audit IDs. A missing identity,
suspended target, existing active administrator, transaction failure, or later
concurrent attempt fails without an admitted result. If command output fails
after commit or the result is otherwise uncertain, inspect the administrator
and immutable audit rows before any retry.

## 8. Secrets and configuration

Required secrets include at minimum:

- PostgreSQL credential or connection secret.
- Authentik OIDC client secret.
- AN-02 cursor keyring with one active 32-byte key and at most one overlapping
  previous key.
- Any session-token hashing/pepper secret if the final implementation requires
  one beyond strong random opaque tokens and stored hashes.

Secret handling requirements:

- Runtime injection through the approved host secret mechanism.
- File permissions or service credentials restricted to the application user.
- Rotation procedure and owner.
- No secret values in process arguments when the platform exposes them.
- No secret values in environment dumps, diagnostics, or support bundles.
- A committed `.env.example` may name variables but contains no working values.
- Compose receives non-secret settings through a root-owned environment file.
  The database URL and OIDC client secret are separate host files mounted
  read-only as Compose secrets; neither appears in the Compose file, image
  configuration, or Docker `Config.Env`.
- The cursor keyring is a separate read-only Compose secret. Only its non-secret
  absolute mount path is configured as `ACTIVITY_CURSOR_KEYRING_FILE`; key
  bytes are never exported by the entrypoint.

### 8.1 AN-02 cursor-key rotation

The cursor keyring uses the strict schema and file validation in the
implementation specification. Secret bytes never enter repository files,
arguments, environment, logs, metrics, retained evidence, or release records.
The record keeps only key IDs, issuance bounds, and secret-file digest or
fingerprint.

To rotate, generate a new 32-byte key with a CSPRNG; atomically install a
complete keyring containing new active and old previous; force-recreate the
application so Docker selects the new secret-file inode; and verify activity
issuance through Caddy. Retain previous until strictly more than 24 hours plus
60 seconds after its last allowed issuance. Then atomically install the
active-only keyring, force-recreate again, and verify. Do not alter
`restart: unless-stopped`, restart PostgreSQL, or introduce a key daemon.

If the structurally valid active key is outside its issuance window, only
`/activity` returns fixed `503`; global readiness and unrelated routes remain
available. An invalid/unreadable keyring is a startup configuration failure.

## 9. Deployment procedure

The alpha runtime uses the exact Compose file from the extracted release. The
required sequence is:

1. Confirm authorization, target environment, release version, and maintenance
   expectations.
2. Capture current application version, artifact digest, migration head,
   configuration version, Caddy config, and health state.
3. Confirm a recent successful backup and known restore procedure.
4. Download or stage the immutable artifact and verify its digest.
5. Validate new configuration without exposing secrets.
6. Put the new artifact beside the current artifact; do not overwrite the only
   rollback copy.
7. Inspect pending migrations and confirm every required Alpha.3 renderer and
   AN-02 projection preflight is present in the exact `gotth-bb-migrate`
   artifact. Do not run it while the old application can still write.
8. For alpha.3, AN-02, or any later renderer/projection migration, enter a
   visible maintenance window, stop the current application, drain in-flight
   requests, and prove the old listener is closed before applying schema or
   content changes. Do not rely on row locks to protect against an old binary
   that can resume afterward and write an obsolete projection version.
9. Run the ordinary argument-free migration command once with an explicit
   result. It first performs the mandatory complete read-only renderer
   preflight and returns before `migration.Apply` if any existing row is not
   classifiable. A failed preflight leaves migration 000007 absent from the
   ledger and installs no renderer state or writer constraint. It uses one
   initial read-only schema/ledger inspection transaction, then one read-only
   transaction per at-most-100-row keyset batch plus a final empty batch.
   Memory and snapshot lifetime are batch-bounded, but total rendering,
   transactions, round trips, and time grow with every post. The stop/drain
   from step 8 must remain in force across those snapshots and the later schema
   transaction because they are separate, not atomic. The alpha.3 `NOT VALID`
   renderer constraint rejects new obsolete-version inserts and updates as soon
   as it is installed; its schema transaction does not scan the posts table.
   The mandatory re-render phase runs even for a fresh empty database and
   validates the constraint only after the batched completion oracle succeeds.
   Final `VALIDATE CONSTRAINT` scans the complete `posts` table while holding
   PostgreSQL `SHARE UPDATE EXCLUSIVE` and the renderer-state row lock; its I/O
   and lock duration grow with population and are not batch-bounded.
   The command emits no per-batch progress. Instead, each mutation transaction
   atomically stores its converted count and last selected signed-bigint post
   ID in `content_renderer_state` with the post updates. `NULL` is the distinct
   before-first-row state, so negative, zero, and `MinInt64` identities are not
   skipped. Restart resumes strictly after the committed cursor; rollback
   advances nothing, and an unknown commit acknowledgement is reconciled from
   this state. The final whole-table oracle and constraint validation remain
   authoritative. The measured
   100-row dense-task compatibility fixture lasted about 20.6 seconds with a
   sampled roughly 73 MiB test-process peak RSS on the development host; it is
   not claimed as a universal worst case. Those selected post rows remain
   locked for the transaction. Run this only while the application is stopped
   and drained. Cancellation is checked between rows and renderer phases,
   rolls back the current transaction, and may still wait for one in-progress
   render phase to return.
   The separate 25,000-post/1,000-topic population fixture records complete
   preflight, schema, 250 conversion batches, final validation/completion,
   exact 251-query/25,000-examined-row mutation-selection proof, total release
   time, and sampled test-process RSS. Treat it as a reproducible
   planning point, not a universal duration or capacity bound. If its measured
   maintenance window is unacceptable for the target installation, stop before
   migration 000007 and plan an explicitly approved maintenance window; do not
   begin the incompatible schema transition and hope it finishes.
   For AN-02 migration 000008, the same stopped/drained boundary covers the
   complete topic/post projection preflight, schema apply, and restart-safe
   backfill. Its six existing-row checks are installed `NOT VALID`; its five
   initially empty partial indexes still perform full heap predicate passes and
   their measured I/O/lock exposure belongs in the release record. Topic then
   post batches commit at most 100 rows with singleton phase/cursor/count state.
   After the post cursor is exhausted and before completion, run explicit
   `ANALYZE public.topics` and `ANALYZE public.posts`. Interruption or a
   competing runner may repeat analysis safely; no runner may mark complete
   before one successful pair. Do not rely on eventual autovacuum before first
   service.
   Completion proves no NULL/partial/stale projection, validates all checks,
   attests the exact indexes and narrowed triggers, and alone marks readiness.
   Its zero-stale oracle and constraint validations scan complete affected
   tables; each validation takes `SHARE UPDATE EXCLUSIVE`, and this completion
   work is population-dependent rather than batch-bounded. Measure it in the
   maintenance-window evidence.
   A PostgreSQL minor update additionally performs the full-corpus byte
   comparison before service. Do not replace this with fixture sampling.
   AN-03 migration 000009 then adds and validates the finite `read_at` check
   and builds the regular partial visible-post index with included `author_id`.
   It performs no read-state backfill, cursor phase, or custom completion step.
   Keep the application stopped while the ordinary migration transaction scans
   the relations and
   holds its schema/index locks; record elapsed time, locks, buffers, relation
   sizes, and I/O. A failed transaction leaves neither change in the migration
   ledger. A legacy `infinity` or `-infinity` value therefore fails closed at
   000008 and requires inspected forward repair; the migration does not silently
   rewrite it. Inspect the ledger and catalogs after an unknown commit outcome
   before retrying.
   AN-04 migration 000010 is also a stopped ordinary migration. Record the
   locks and scans used to add/validate positive revisions and replace audit
   checks, then attest the seeded settings tuple and exact runtime grants. A
   failed transaction leaves the ledger at 000009; after an unknown outcome,
   inspect the ledger, singleton, revisions, constraints, and grants before any
   retry.
10. Before starting the application, the migration owner must apply the exact
   packaged `deploy/postgresql/runtime-grants.sql` with the deployment's
   restricted runtime role as psql's `runtime_role` variable. This is required
   after 000007 because PostgreSQL grants a newly created table only to its
   migration owner by default; the application readiness role needs the
   artifact's narrow `SELECT` on `content_renderer_state`. Reapplying this
   `GRANT` artifact is idempotent. After 000008 it also supplies the exact
   read-only `search_projection_state` grant. Do not transfer table ownership
   or substitute table-wide mutation privileges.
11. Build the application image from the verified archive and verify labels and
   database-free binary identities.
12. Validate the resolved Compose model without printing its environment.
13. Preserve the running PostgreSQL container and durable bind mount and start
   only the new application container.
14. Wait for container health and application readiness.
15. Run deployed smoke tests through Caddy at `https://bb.alhstudios.com/`.
16. Record result, version, image ID, migration head, and evidence.
17. If any gate fails, stop and execute the documented rollback/repair decision.

Deploy commands must be safe to rerun or must detect completed state. A retry
must not duplicate migrations, seed users, or moderation data.

## 10. Smoke test

Every deployed prerelease verifies:

- `/` serves the public index through the dedicated Caddy site.
- Complete pages show the validated release version and bounded page/template
  render durations; HTMX fragments do not duplicate the page footer.
- Public index behavior matches site policy.
- Authentik login begins with the correct callback and returns successfully.
- Eligible member local provisioning succeeds.
- Public, authenticated, and group-restricted areas show only to expected
  actors.
- Read-only and archived publishing are rejected correctly.
- Topic/reply creation and readback work.
- Restricted direct URL and list leakage checks pass.
- Once AN-02 is present, public/member/group/staff search, recent activity,
  continuation, direct-post, expiry, and restricted-occupancy checks pass
  through Caddy without exposing query/cursor values in application logs.
- Once AN-03 is present, a member sees exact authorized new/unread state,
  first-unread navigation, and a CSRF-protected mark-read action; a visitor
  receives no personalized state; restricted topics do not alter counts; and
  ordinary GETs leave markers unchanged.
- Once AN-04 is present, verify the public rules page and current shell
  presentation, then as a designated administrator page accounts/groups/areas,
  change and restore one disposable account role, grant and revoke one
  disposable membership and area restriction, archive/restore one disposable
  area, and reconcile dashboard counts. Confirm every mutation audit and target
  session revocation without using production identities or content as test
  fixtures.
- Logout revokes the local session.
- Liveness/readiness and structured request IDs are observable to operators.

Alpha may use disposable test content. Production smoke tests use designated
non-destructive fixtures and do not pollute normal discussions.

## 11. Rollback

Application rollback is allowed only when the previous artifact is compatible
with the current database schema. The release record identifies that boundary.

Decision order after failure:

1. If the new process failed before migration, restore the previous artifact
   and configuration.
2. If migrations ran and are backward-compatible, restore the previous
   artifact and keep the expanded schema.
   Alpha.3's renderer writer constraint is not backward-compatible with an old
   binary that persists the previous renderer version; do not restart that
   binary after migration. Use forward repair, or restore the pre-migration
   database backup before restoring the old artifact.
   AN-02 migration 000008 likewise makes the prior artifact fail its exact-head
   readiness contract. Use the current artifact/forward repair or restore the
   verified pre-000008 database backup; there is no down-migration claim.
   AN-03 migration 000009 is additive but exact-head readiness still prevents
   the prior artifact from starting. Use the current artifact/forward repair or
   restore the verified pre-000009 database backup; do not invent a down
   migration or delete private marker state by hand.
   AN-04 migration 000010 likewise changes exact-head readiness, audit checks,
   and administration revisions. Use the current artifact/forward repair or
   restore the verified pre-000010 database backup; an older artifact must not
   mutate the expanded administration schema.
3. If migration outcome is unknown, inspect migration and database state before
   any retry.
4. If migration is incompatible but reversible without data loss, execute the
   reviewed down/repair procedure.
5. If rollback would destroy data, stop writes and choose forward repair or
   restore from backup with explicit owner approval.

Never advertise `down` as a rollback merely because the migration tool supports
the command.

For the alpha container transition, application rollback stops and removes only
the application container, restores the previous host environment if it was
changed, and starts the preserved native `gotth-bb.service`. The PostgreSQL
container and `/tank/gotth-bb/postgres17` are not removed. `docker compose
down -v`, manual volume deletion, and database-container recreation are not
application rollback commands.

## 12. Backup and restore

Backups cover:

- PostgreSQL data and schema.
- Required runtime configuration, excluding secrets from broad archives.
- Secret-recovery mechanism or separately protected secret backup.
- Caddy and service-manager configuration.
- Release records and artifact references.

Requirements before stable 1.0:

- Automated scheduled PostgreSQL backups.
- Copies stored outside the application host failure domain.
- Retention policy and encryption appropriate to forum content.
- Backup job failure alerting.
- Restoration into a clean PostgreSQL instance.
- Application smoke test against restored data.
- Measured recovery time and recovery point.

A backup file's existence proves nothing until restoration is tested.

## 13. Observability

### Logs

- Structured service startup/shutdown, request, authentication result,
  moderation transition, migration, and background-cleanup events.
- The application container uses Docker's journald logging driver with a stable
  tag; operators can also use bounded `docker compose logs` output.
- Request IDs propagated through error pages and HTMX errors.
- No tokens, cookies, secrets, or unrestricted content bodies.
- AN-02 application events retain only route pattern, fixed outcome, status,
  duration, and bounded counts. They exclude filters, cursor values, identities,
  result fields, and snippets. Caddy still receives the raw query-bearing
  request target, and operator-enabled PostgreSQL statement/parameter
  diagnostics may receive bound search values. Access and retention for those
  separate systems must be set accordingly; application redaction does not
  make a broader claim.

### Metrics

- Request rate, status, and latency by route name.
- Active/idle PostgreSQL pool state and query-error count.
- Login success/failure class without token or identity leakage.
- Session creation/revocation/expiry counts.
- Publishing and moderation error counts.
- Rate-limit rejection counts.
- Backup age and last restore rehearsal.

Metrics labels are bounded. User IDs, topic IDs, paths containing identifiers,
and group names do not become unbounded labels.

### Alerts

Initial actionable alerts cover:

- Service not ready or repeated restart.
- Sustained server-error rate.
- PostgreSQL unavailable or pool exhausted.
- Migration mismatch.
- Backup failure or excessive backup age.
- Disk/capacity threshold.
- Authentik login failures above an owner-approved threshold.

## 14. Routine operation

- Remove expired OIDC login attempts and sessions in bounded batches.
- Review rate-limit and moderation signals without collecting unnecessary
  personal data.
- Test supported upgrade paths before deploying dependencies or PostgreSQL
  changes.
- Review audited forum-local role and group assignments after access-policy
  changes.
- Rehearse restore on a schedule.
- Keep release artifacts and known-good commit references long enough to meet
  rollback policy.
- Apply security fixes through a documented patch release, not an untracked
  production edit.

## 15. Incident handling

### Suspected authorization leak

1. Preserve logs and release identity without copying restricted content more
   broadly.
2. Disable or restrict the affected route at the narrowest safe boundary.
3. Determine all surfaces sharing the defective query/policy.
4. Fix and test the complete leakage inventory, not only the reported URL.
5. Review access logs and notify the owner of confirmed exposure.

### Compromised OIDC client secret

1. Rotate/revoke in Authentik.
2. Update the runtime secret through the approved mechanism.
3. Restart/reload safely.
4. Review login events and session policy.
5. Do not commit the replacement or paste it into issue evidence.

### Database failure

1. Stop unsafe writes or remove readiness.
2. Establish whether commit outcomes are known.
3. Recover service or restore to a clean database according to the tested plan.
4. Verify migration head and smoke tests before readiness.

### Bad release

Use the rollback decision in section 11 and preserve the failed release's logs,
artifact digest, and migration result for review.

## 16. Alpha.1 operational readiness checklist

- [x] Target host and service manager selected: `development`, systemd.
- [ ] Inbound reachability, firewall, and TLS verified.
- [ ] Existing Caddy configuration captured and validated with the
      `bb.alhstudios.com` site.
- [ ] PostgreSQL database, runtime role, migration role, and backup location
      created.
- [ ] Restricted runtime grants applied and the governance singleton lock
      exercised through the runtime role.
- [ ] Authentik client, callback, approved claims, and test identities configured.
- [ ] First local administrator granted by the audited operator command.
- [ ] Runtime secrets installed outside the repository.
- [ ] Immutable artifact built and digest recorded.
- [ ] Fresh migrations and preflight pass.
- [ ] Service starts and readiness passes.
- [ ] Complete alpha smoke test passes.
- [ ] Rollback artifact/configuration available.
- [ ] Deployment record and known limitations published.

This checklist is a plan, not evidence that any item has already occurred.

## 17. Known-good reference

After deploying a candidate fix or release, the owner is asked whether it works
in the real user workflow. Only after owner confirmation is the exact commit,
artifact digest, migration head, and configuration schema recorded as a known-
good reference. Passing automation alone does not claim that user confirmation.

## 18. Open operational decisions

Before alpha deployment, the owner must select or confirm:

1. Inbound port-forwarding path to the Caddy host, if the public DNS target is
   not terminated directly on `development`.
2. PostgreSQL backup destination; PostgreSQL 17 remains the supported alpha
   contract and must not be silently replaced by the host's PostgreSQL 18.
3. Authentik client and approved profile claims; the deployed issuer host is
   `https://auth.dannyhunn.com`.
4. Initial administrator issuer/subject, alpha access policy, and test users.
5. Session/revalidation lifetimes.
6. Soft-deletion and audit retention.
7. Monitoring and alert destination.
