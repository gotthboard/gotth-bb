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
`gotth-bb-VERSION-GOOS-GOARCH/` and include the four binaries,
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
    reverse_proxy 127.0.0.1:18082 {
        header_up X-Forwarded-For {remote_host}
        header_up -Forwarded
        header_up -X-Real-IP
    }
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
8. Inspect the adapted configuration and prove a caller-supplied forwarding
   header is overwritten, not appended or trusted.

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
  `runtime_role` variable. This is a complete idempotent runtime ACL, not an
  incremental grant list: it first removes inherited or restored table and
  sequence privileges, then grants the exact Beta.1 read, mutation, sequence,
  and schema boundary. This makes a `--no-privileges` logical restore usable
  and removes broader Alpha default privileges before service.
- The governance entry grants only `UPDATE(singleton)`, which PostgreSQL
  requires for `SELECT ... FOR UPDATE`, plus `SELECT` on the migration-owned
  readiness singletons. Table-wide governance UPDATE, UPDATE on `created_at`,
  renderer/search-state mutation, and DELETE remain denied.
- After 000008, the same packaged artifact's search-state entry adds only
  `SELECT` on the migration-owned `search_projection_state` singleton for
  runtime readiness; runtime never owns or mutates that table.
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
  runtime ACL adds settings SELECT/column-UPDATE, group SELECT/create/
  rename plus `forum_groups_id_seq` usage, and membership SELECT/insert/delete.
  It preserves the pre-AN-04 baseline, adds no settings insert/delete/key
  update, adds no mapping update, and does not grant deletion of users, groups,
  areas, settings, or audit rows.
- Migration 000011 adds the consistent publication-window tuple and validates
  finite account-creation time because account age now selects security policy.
  Its constant count default avoids rewriting existing rows, while both check
  validations still scan and lock the relation. A legacy nonfinite
  `users.created_at` aborts the entire migration; inspect and apply a reviewed
  forward data repair before retrying rather than inventing account age. The
  runtime ACL
  adds UPDATE only on `publication_window_started_at` and `publication_count`;
  it grants no table-wide account mutation.
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
- B1-09's dedicated Authentik Board-control API token, distinct from the OIDC
  client secret and from every administrator token.
- The SMTP password when the shared Authentik/Board transport authenticates.

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
- AN-05's non-secret abuse-rules file is a separate root-owned, read-only bind
  mounted at `/run/config/gotth-bb-abuse-rules`; only that container path is
  configured as `ABUSE_RULES_FILE`. Record its SHA-256 and the seven exact rate
  values without copying rule contents into deployment logs or evidence.
- B1-09 mounts the Authentik control token only into the isolated control
  gateway and authoritative Authentik server container where operator-invoked
  bootstrap executes, never Board. The
  optional SMTP password and independent invitation-fingerprint key are
  separate Compose secrets. Its non-secret control-object JSON is a root-owned
  read-only bind shared by Board and the gateway only after exact UUID/slug
  attestation. No secret is passed on a process argument, copied into an image,
  rendered by `docker compose config`, or exposed in the Board container.
  The token file is `root:65530` mode `0440`, the attested object descriptor is
  `root:65531` mode `0440`, and the Board-only fingerprint key is
  `root:65532` mode `0440`; these service groups are exact and are not reused as
  ordinary runtime groups.
- The application derives invitation request fingerprints from the independent
  256-bit Board-only fingerprint key under a fixed HMAC domain. Control-token
  rotation is blocked
  while any invitation create operation remains `creating` or `unknown`;
  reconcile it to a terminal/active state first, then rotate the Authentik token
  and gateway secret mount as one stopped-writer change. The independent
  invitation-fingerprint key does not rotate with the Authentik token.

### 8.1 AN-05 abuse-policy update

Prepare a complete canonical rules file offline, validate it with the exact
release binary, and atomically replace the host file without changing its
owner or mode. Update rate values only in the root-owned environment file.
Force-recreate the one application container so both immutable settings and
the new file inode take effect together; do not restart PostgreSQL. Verify the
effective fixed profile through disposable requests and record file digest,
values, application identity, and result. A malformed file or setting must
leave the replacement process unable to start while the prior container and
file remain the rollback pair. Do not edit a mounted inode in place.

### 8.2 AN-02 cursor-key rotation

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
   checks, then attest the seeded settings tuple, exact AN-04 grant delta, and
   explicit forbidden operations. A failed transaction leaves the ledger at
   000009; after an unknown outcome, inspect the ledger, singleton, revisions,
   constraints, and grants before any retry.
   AN-05 migration 000011 is another stopped ordinary migration. Record the
   user-relation lock and validation scans for finite account-creation time and
   the constant-size publication tuple, then attest its exact defaults, both
   checks, and the narrow column-UPDATE grant. Existing publication tuples must
   remain `(NULL, 0)`. A failed transaction leaves
   the ledger at 000010; after an unknown outcome, inspect ledger, columns,
   constraint, tuples, and grants before retrying.
10. Before starting the application, the migration owner must apply the exact
   packaged `deploy/postgresql/runtime-grants.sql` with the deployment's
   restricted runtime role as psql's `runtime_role` variable. This is required
   after a privilege-free restore and also closes broader legacy/default ACLs.
   Reapplying the complete ACL is idempotent. It supplies read-only renderer
   and search state, exact settings/group/membership authority, and the named
   user-column mutations including the two publication-window columns. Do not
   transfer ownership or substitute table-wide account/state mutation.
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
- Complete pages show exactly `Powered by GOTTH Board` linked to
  `https://github.com/gotthboard`, even when the configured site name and home
  URL differ; no tenant name or deployment home target enters attribution.
- Authenticated administrator smoke covers `/admin/areas` and one area detail
  at phone and desktop widths: controls remain visible and labeled, the mobile
  form does not overflow horizontally, desktop grouping is bounded, and group
  access cards remain operable. No form is submitted during visual smoke.
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
- Once AN-05 is present, verify Caddy overwrites client identity; health/static
  exemptions; a disposable client's bounded request rejection; established and
  new-account publication rejection/expiry; allowed and blocked domain/exact-
  URL drafts through preview, post mutation, and disposable community-rules
  update; fixed `Retry-After`; and absence of
  client/account/content/rule values from application logs. Restore the exact
  configured rules file and rate profile after the smoke test.
- Once B1-09 is present, verify closed/open/approval/invitation mode agreement
  at Board and direct Authentik URLs; one disposable pending approval and one
  expiring invitation; restrictive suspension/reinstatement reconciliation;
  tightened and restored session/publication policy; maintenance entry and
  administrator recovery; bounded session view/revocation; configured email
  status and one self-addressed test result; permission negatives; audit redaction;
  and restoration of the exact pre-smoke control settings.
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
   AN-05 migration 000011 changes exact-head readiness, requires finite account-
   creation time, and makes publication counters part of every topic/reply
   commit. Use current artifact/forward
   repair or restore the verified pre-000011 database backup; an older artifact
   must not publish without the counter.
   B1-08 migration 000012 changes the external issuer/subject and audit contract;
   rollback requires the paired prior Board/Auth state described by the
   standalone procedure, never an older binary against the rebound identity.
   B1-09 migration 000013 makes durable control/session/publication state part
   of request authority. Use the current artifact/forward repair or restore the
   verified paired pre-000013 Board and Authentik backups plus their matching
   protected configuration. Beta.1.5 must not run at head 000013.
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
- AN-05 emits only `request_rate`, `request_capacity`, `publication_rate`, and
  `blocked_destination` rejection classes with route, request ID, status, and
  bounded retry seconds where applicable. Client address/digest, account
  identity/age, raw target/query/body, Markdown, destination, matched rule,
  configured count, and limiter occupancy are forbidden.

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

## 18. Beta.1 release procedure

Beta.1 upgrades the actual active Alpha.2 container deployment. The disabled
native Alpha.1 systemd unit is retained historical state, not an active service
and not a valid rollback claim. The preflight release record resolves the live
container, image, Compose project/configuration, loopback listener, Caddy
adapted configuration, PostgreSQL container/image/data mount, migration head,
grants, and current public smoke state before any mutation.

Proceed in this order:

1. Verify the annotated `1.0.0-beta.1` tag resolves to the admitted merged
   `main` commit and both canonical/mirror remotes agree.
2. Build twice from the tagged archive; require byte-identical packages and
   matching version/commit identity in forum, migration, and operator binaries.
   Record package, checksum-file, SBOM/dependency, image, and source digests.
3. Resolve the current live Alpha.2 Compose configuration and environment file
   without printing secrets. Record the existing application image and a
   schema-compatible rollback decision rather than merely naming the disabled
   systemd service.
4. Verify durable-mount identity, PostgreSQL 17 health/version, free space,
   database identity, exact migration head, current runtime grants, readiness,
   and the pre-upgrade smoke matrix.
5. Create and verify a fresh logical database backup plus a non-secret
   configuration/release inventory. Record digests, destination failure domain,
   elapsed time, and the precise restoration command without embedding a
   database URL or secret.
6. Restore that backup into a clean task-owned PostgreSQL 17 instance. Rehearse
   the complete stopped upgrade through 000011, renderer/search completion,
   packaged runtime grants, readiness, application smoke, and Beta logical
   backup/clean restore. Do not continue on any missing row, grant, digest,
   readiness, or rollback evidence.
7. Stop and drain only the active application container. Preserve the
   PostgreSQL container, bind mount, Caddy, secrets, previous application image,
   release directory, and Compose configuration.
8. Create and verify the exact pre-upgrade database backup again after the
   application is stopped. Run the packaged migration/completion commands and
   packaged runtime grants using secret files or protected environment input,
   never process arguments or broad logs.
9. Inspect migration head, renderer/search completion, schema constraints,
   runtime grants, and critical row/count continuity before starting Beta.1.
10. Validate the resolved Beta Compose model without printing its environment;
    start only the new application container from the recorded image digest.
11. Prove health, nonroot/read-only/capability hardening, loopback listener,
    journald logging, Caddy identity overwrite, Authentik callback/revalidation,
    and the complete Beta smoke/leakage/accessibility matrix.
12. Record rollback feasibility against the now-current schema. If the previous
    Alpha.2 artifact cannot run at migration head 000011, rollback means restore
    the verified pre-upgrade backup before restarting that artifact or apply a
    reviewed forward repair; do not start it optimistically.
13. Publish the release record and known limitations for designated test users.
    Ask the owner to exercise the real browser workflow. Only an affirmative
    answer records the commit, artifact/image digest, migration head, and
    configuration identity as known-good.

The initial Beta backup/restore gate may honestly use same-host backup storage
for the restricted test deployment when that failure domain is published.
Automated scheduling, off-host copies, retention, encryption policy, alerting,
and production RPO/RTO remain stable-release work. Same-host storage is not
described as disaster recovery.

### 18.1 Beta.1 release checklist

- [ ] Admitted merged commit and both remotes agree.
- [ ] Annotated Beta.1 tag resolves to that commit.
- [ ] Two package builds are byte-identical; identities and digests recorded.
- [ ] SBOM/dependency and secret scans pass.
- [ ] Active Alpha.2 container/database/configuration baseline captured.
- [ ] Fresh stopped pre-upgrade backup verifies.
- [ ] Clean-instance Alpha.2-to-Beta.1 upgrade rehearsal passes.
- [ ] Clean-instance Beta logical backup/restore rehearsal passes.
- [ ] Live stopped migrations/completions and runtime grants pass.
- [ ] PostgreSQL container and durable mount identities are preserved.
- [ ] Only the application container is replaced.
- [ ] Health, hardening, Caddy, Authentik, complete smoke, leakage, and
      accessibility checks pass.
- [ ] Prior artifact, pre-upgrade backup, and exact rollback decision retained.
- [ ] Release record and known limitations published.
- [ ] Owner confirms the real workflow before known-good is recorded.

### 18.2 Beta.1 corrective successor

If a defect is found after a Beta.1 candidate is published but before owner
confirmation, do not rewrite or delete any published tag. A correction that
remains inside the admitted Beta.1 product and trust boundary uses the next
unused canonical SemVer identity in the `1.0.0-beta.1.N` sequence. Its record
must name every superseded candidate, demonstrate a negative control on the
immediately failed tree and a positive control on the repair, pass the affected
admission gates and two fresh cold reviews, then use a guarded merge and
annotated successor tag.

When the correction has no data or permission change, deployment replaces only
the application container, preserves PostgreSQL/Caddy/configuration identity,
retains the failed image and backups, and repeats exact artifact identity,
hardening, Caddy, smoke, log-redaction, and affected browser/accessibility
checks. Previously published content-addressed assets remain served for their
promised cache lifetime. The corrective successor still requires affirmative
owner confirmation before it becomes known-good. This procedure does not admit
RC.1 or stable work.

### 18.3 Beta.1 standalone-stack corrective successor

Beta.1.2 remains immutable but is not known-good because its running package
depends on a host Caddy and a shared Authentik tenant. Its successor must use
the packaged standalone Compose model and the exact sequence below:

1. Render and validate the complete Compose and stack-Caddy configurations with
   non-secret test values. Require six long-running services: Caddy, Board,
   Board PostgreSQL, Authentik server, Authentik worker, and Authentik
   PostgreSQL. Reject mutable image references, Docker-socket mounts, a public
   application/Auth/database port, or shared durable paths.
2. On a clean task-owned host namespace, create independent secrets and empty
   durable paths. Start both databases and Authentik only. Apply the packaged
   blueprint twice; prove byte-identical desired state, preserved provider
   secret, `user_uuid` subject mode, exact redirect/launch/enrollment URLs, the
   exact Board application/provider/access group, and no unrelated non-built-in
   application or provider.
3. Migrate a disposable Alpha/Beta Board database with the application stopped,
   apply exact runtime grants, provision one dedicated Authentik identity, and
   run the operator identity rebind. Prove one exact identity changed, every
   prior session was revoked, one immutable operator audit row was appended,
   and user/role/group/content counts and ownership did not change.
4. Start Board and stack Caddy. On a single-purpose host prove only stack Caddy
   accepts non-loopback traffic. On a multi-site host prove stack Caddy accepts
   only the two dedicated loopback listeners and the retained TLS edge routes
   only the exact Board/Auth hostnames without changing unrelated sites. The
   Board and Authentik hostnames route to their respective loopback upstreams;
   caller forwarding headers are overwritten; OIDC discovery, authorization,
   callback, revalidation, logout, and an Authentik outage fail-closed path work;
   and the full Beta smoke matrix remains clean.
5. Restart the entire project without recreating durable paths. Require exact
   image/configuration identity, healthy services, idempotent blueprint state,
   unchanged Board/Auth identities, and a repeated smoke pass.
6. Create separate digested logical backups of Board and Authentik PostgreSQL
   plus a non-secret inventory of Caddy state/configuration and required secret
   references. Filesystem archives containing Caddy/Auth state are root-owned
   mode 0600 under a mode-0700 backup directory and preserve numeric ownership,
   ACLs, and extended attributes. Restore both databases into clean task-owned
   services, restore required non-database state, reapply the packaged Board
   runtime grants, and
   repeat identity and smoke checks at the exact original Board/Auth origins.
   A substituted recovery hostname or port changes the OIDC issuer and is not
   recovery evidence. Same-host storage remains an explicit Beta limitation,
   not disaster recovery.
7. Rehearse rollback after cutover: stop the candidate, restore the exact
   stopped Board backup containing the prior issuer binding, and start the
   retained Beta.1.2 topology without mixing old and new identity state. Then
   restore the candidate pair and prove forward recovery. Never use
   `docker compose down -v`.
8. Only after exact-tree gates and two fresh CLEAN reviews may the successor be
   merged, mirrored, tagged, packaged, and staged for live cutover. Before
   touching live DNS, Caddy, Authentik, or the Board database, take and verify
   fresh stopped backups and retain the old configuration, containers, images,
   provider, and durable paths.
9. The live cutover changes one boundary at a time, records every resolved
   identity, digest, port, path, and rollback decision without secret values,
   and repeats health, login, revalidation, leakage, accessibility, restart,
   backup, and rollback-readiness checks. The old shared-tenant Board objects
   are disabled or removed only after owner acceptance and a retention period;
   unrelated shared-tenant state is never changed.

The dedicated Authentik hostname and issuer are deployment identity. They must
resolve to the stack Caddy and appear in the blueprint, Board configuration,
and release record exactly. Changing them after admission requires a new
identity cutover; aliases and silent issuer normalization are forbidden.

The multi-site edge is not the Board routing implementation. Its admitted
configuration terminates TLS, preserves the original `Host`, overwrites
`X-Forwarded-For` with exactly its remote peer, removes `Forwarded` and
`X-Real-IP`, and proxies the two exact hosts to stack-Caddy loopback ports.
Stack Caddy owns the Board/Auth split and upstreams, repeats alternate-header
removal, supplies the canonical client identity to Board, and overwrites
`X-Forwarded-Proto` with the exact public HTTPS scheme for both upstreams.
Replacing the host edge with the stack container on a multi-site host is
forbidden because it would evict unrelated userspace.

### 18.4 B1-09 Board administration control-plane successor

B1-09 is a schema, permission, identity-flow, and application change. It is not
an application-only corrective deploy. It may proceed to live mutation only
after Beta.1.5 owner acceptance closes B1-08G and the exact B1-09 candidate has
two fresh CLEAN final reviews.

Proceed in this order:

1. Resolve the exact running Beta.1.5 commit/tree/image, six container/image
   identities, both database heads and mounts, blueprint objects, Caddy routes,
   root-owned files, and current smoke. Verify B1-08 retained rollback state.
   Before migration, prove every existing local external identity is represented
   in the dedicated accepted group; any mismatch blocks rather than poisoning
   the sync-state backfill.
2. Generate a new independent 256-bit control token and a distinct 256-bit
   invitation-fingerprint key into separate root-owned protected files. Prepare
   the non-secret SMTP values, optional separate password secret, and a
   gateway-owned `65533:65531` mode-0750 socket directory. Grant dedicated
   control GID `65531` only as a supplemental group to Board, which mounts the
   directory read-only. Never reuse the OIDC client,
   Authentik bootstrap, superuser token, or either B1-09 secret for another
   purpose.
3. On a clean task-owned standalone stack, apply the B1-09 blueprint twice,
   emit and validate the exact control-object JSON, run the complete permission
   positive/negative matrix, reapply to prove bounded stale invitation-permission
   cleanup without group/RBAC drift, and prove all registration and email
   journeys.
4. Back up Board and Authentik databases separately plus protected Caddy/Auth
   state/configuration inventories. Verify digests and clean matching-major
   restores before any live schema or blueprint mutation.
5. Stop the live Board writer. Apply migration 000013 and runtime grants while
   registration remains closed. Apply the Authentik blueprint using the new
   control token, validate exact objects/permissions including the documented
   raw-token `send_email` and self-token-lifecycle excesses, install the
   control-object file, and render the
   complete Compose configuration without secret values. Prove Board has no
   control-token mount, environment value, or descriptor, and prove the gateway
   has no Board-database or SMTP credential/configuration/secret mount.
6. Start the isolated gateway first and require its Unix socket owner, group,
   mode, gateway-owned non-Board-writable directory, peer-UID rejection, closed
   route surface, denial to Board's ordinary UID/GID without supplemental
   control GID `65531`, request/concurrency bounds, and Authentik probe. Then
   start the candidate Board application. Require readiness head 000013,
   ceiling equality, control-object identity, gateway-only API probes, and
   closed registration before enabling any other mode.
   If a prior gateway died without unlinking its socket, stop and prove no
   process owns the socket, capture its device/inode, recheck the unchanged
   device/inode immediately before removing that exact path, and then restart;
   the container entrypoint never removes an incumbent path itself.
7. Run the B1-09 smoke matrix with disposable identities/content. Restore the
   exact pre-smoke control settings and keep registration closed unless the
   owner separately chooses another live mode. A test email goes only to the
   current designated administrator's verified address.
8. Restart the complete stack and repeat readiness, direct-flow denial, OIDC,
   session, maintenance recovery, email status, identity reconciliation, and
   data/permission checks. Then rehearse rollback from the verified paired
   pre-000013 backups and forward recovery to the candidate. Never combine one
   generation's Board database with another generation's Authentik identity
   state.
   Maintenance recovery must prove both the ordinary current-administrator form
   and the packaged stopped-writer, migration-owner database command. The latter
   is the explicit fallback when Authentik outage or expired revalidation makes
   browser recovery impossible; maintenance never bypasses normal identity
   revalidation.
9. Guarded fast-forward/mirror, successor Beta tag, reproducible package/image,
   live evidence, and owner physical acceptance bind to one exact commit. No
   prior backup, secret, object file, image, or tag is removed before acceptance
   and the later approved retention boundary.

A failed blueprint or permission probe leaves registration closed and the old
application untouched. After 000013 commits, Beta.1.5 cannot run against the
new exact-head contract; rollback restores both verified databases and the
complete prior protected configuration before starting the retained image.
Unknown Board commit, Authentik API, SMTP, or backup outcomes are inspected and
classified before retry. External email is never retried automatically.

## 19. Operational decisions

The Beta.1.2 deployment resolved the following baseline before B1-08 without
placing secret or personal values in the repository:

- the public Caddy/TLS path terminates on `development` and proxies to the
  loopback-only application listener;
- the application uses the dedicated PostgreSQL 17 container and durable bind
  mount, not the host's PostgreSQL 18 installation;
- the dedicated Authentik client, approved profile claims, first administrator,
  and designated test identities exist in protected deployment/audit state;
- session maximum age is 24 hours, idle timeout is 8 hours, and Authentik
  revalidation interval is 30 minutes; and
- the initial restricted-Beta logical backup destination remains the existing
  root-owned same-host board backup area, with that failure domain published as
  a limitation.

Changing any resolved identity, lifetime, listener, database, or backup
boundary requires explicit owner approval and fresh affected evidence.

The owner's B1-08 instruction explicitly replaces the shared-Caddy/shared-
Authentik topology with the standalone-stack contract. It does not alter the
30-minute revalidation maximum, public-area policy, retention policy, or
off-host backup boundary.

B1-09 retains the deployed 24-hour maximum session age, eight-hour idle
ceiling, 30-minute Authentik revalidation ceiling, and AN-05 rate ceilings.
Board may tighten the admitted dynamic values but cannot widen those deployment
ceilings. Migration 000013 seeds registration closed. Enabling open, approval,
or invitation enrollment requires verified SMTP and is an explicit audited
administrator action, not a release-script default.

The following decisions remain open for their later affected behavior:

1. Whether Beta test users receive public areas or only the existing restricted
   area policy.
2. Soft-deletion and audit retention duration.
3. Scheduled/off-host backup destination, retention, encryption policy, and
   failure alerting before stable.
4. Monitoring and alert destination plus owner-approved thresholds.
