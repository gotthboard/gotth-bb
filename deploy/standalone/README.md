# Standalone deployment

This directory is the version 1.0 seven-service deployment boundary. It owns
Caddy, GOTTH Board, the isolated Authentik control gateway, Board PostgreSQL,
Authentik server/worker, and Authentik PostgreSQL. It never shares Caddy
configuration/state or an Authentik tenant.
On a single-purpose host its Caddy binds the public HTTPS endpoints directly.
On a multi-site host, the existing TLS edge may forward only the two public
hostnames to loopback listeners owned by this Caddy container.

The examples contain no working secret. Copy both example environment files to
root-owned deployment state outside the release directory, replace every
example hostname/path/image/subject, and create all required durable directories
before rendering Compose. Caddy's data and config directories must be writable
by UID/GID 65532. The Board PostgreSQL directory is UID/GID 999, the pinned
Alpine Authentik PostgreSQL directory is UID/GID 70. Authentik data, templates,
and certificate directories are UID/GID 1000 with modes 0770, 0700, and 0750
respectively, matching the pinned worker's startup contract. The database
directories are mode 0700.
Secret files must be regular root-owned mode-0440 files: use
group 999 for both PostgreSQL passwords, group 1000 for the Authentik instance
key, and group 65532 for the Board database URL, OIDC secret, and cursor
keyring. The shared SMTP password file is root/group 65529 mode 0440; it is an
exact empty regular file while SMTP is disabled or unauthenticated and contains
the bounded password bytes without framing only for authenticated SMTP. The
Board application and both Authentik processes receive supplemental group
65529; the control gateway never does. The pinned Authentik server receives
supplemental groups 999 and 65532; the worker receives only 999 besides the
SMTP group. These numeric identities are part of the pinned-image verification
gate, not guesses about mutable tags. The Board
PostgreSQL directory must not overlap the Authentik PostgreSQL directory.
Every nonempty secret file contains the exact secret bytes with no trailing
newline, carriage return, or other framing.

The Board database URL secret names `gotth_bb_runtime` and the loopback
maintenance port. Its password must equal the Board runtime password secret.
The migration URL is operator input and is never stored in Compose. The OIDC
client secret file is shared only by the Board application and Authentik server;
the worker does not receive it. The pinned Authentik worker does not honor the
documented `file://` password form consistently in its Rust database path. The
packaged non-root entrypoint therefore reads the two mounted Authentik secrets,
validates their framing, exports them only into the Authentik process, and
replaces itself. Docker image/configuration metadata retains no secret value.
The same root-owned SMTP tuple is copied into `app.env` and the
`GOTTH_BB_SMTP_*` deployment values. Preflight rejects any difference,
derives Authentik's TLS booleans from the closed Board mode, and requires the
same whole-second timeout. `starttls` maps to `true:false`,
`implicit_tls` to `false:true`, and test-only `plain` to `false:false`.
Production plain SMTP is rejected. Authentik reads the shared password through
its non-root entrypoint; Board reads the same mounted bytes only when
`SMTP_PASSWORD_FILE=/run/secrets/smtp_password`.

Set `GOTTH_BB_COMPOSE_ENV_FILE` to the absolute deployment-environment path.
Run the root-only preflight before starting anything. It rejects mismatched
public/OIDC origins, overlapping durable paths, incorrect numeric ownership or
modes, secret framing, listener collisions, mutable Board image naming, a local
image ID or release-label mismatch, and an invalid Compose render. Record the
exact `sha256:` image ID printed by packaged `build-image.sh` in
`GOTTH_BB_IMAGE_ID`; a matching-looking mutable tag is not custody:

```sh
sudo deploy/standalone/preflight.sh "$GOTTH_BB_COMPOSE_ENV_FILE"
```

For a fresh install, start both PostgreSQL services and Authentik server/worker
first. Wait for health, then run `apply-authentik.sh` twice and require the exact
line `AUTHENTIK_BOARD_BLUEPRINT_APPLIED` both times. The import uses the mounted
OIDC secret in memory and does not write a secret-bearing blueprint.
Production uses the default `gotth-bb-standalone` Compose project. A concurrent
isolated rehearsal may pass its explicit lowercase project name as the helper's
only argument; the helper never guesses a running project. The app, Authentik,
PostgreSQL, and Caddy-admin loopback ports can likewise be assigned distinct
rehearsal values without weakening the production default bindings.

The direct deployment tuple uses public HTTPS origins, bare hostname Caddy
addresses, `GOTTH_BB_CADDY_BIND=0.0.0.0`,
`GOTTH_BB_CADDY_CLIENT_ADDRESS={remote_host}`, and
`GOTTH_BB_CADDY_UPSTREAM_SCHEME=https`. A shared-host deployment uses
the same public HTTPS origins, `http://HOST:PORT` Caddy addresses,
`GOTTH_BB_CADDY_BIND=127.0.0.1`, and
`GOTTH_BB_CADDY_CLIENT_ADDRESS={http.request.header.X-Forwarded-For}`, and
`GOTTH_BB_CADDY_UPSTREAM_SCHEME=https`. The
outer TLS edge must overwrite `X-Forwarded-For` with exactly `{remote_host}`
and remove `Forwarded` and `X-Real-IP`; it must preserve the original `Host`.
The inner Caddy repeats alternate-header removal and supplies the one canonical
address and explicit HTTPS scheme to Board and Authentik. The loopback-only
HTTP rehearsal instead requires `GOTTH_BB_CADDY_UPSTREAM_SCHEME=http`.
Arbitrary proxy chains are not admitted.

Complete Authentik's initial-setup flow through the dedicated Authentik origin,
then create or approve only the designated Board users and add them to
`gotth-bb-users`. Record each user's Authentik `uuid`; the database primary key
and a subject from another Authentik instance are not OIDC subjects for this
stack. Set `BOOTSTRAP_ADMIN_SUBJECT` to the exact designated administrator UUID
before the first Board login. An upgrade from another issuer uses the packaged
`gotth-bb-operator rebind-external-identity` command while the Board application
is stopped; do not create a second forum user.

Run the release's migration binary against the migration-role URL, apply the
packaged runtime grants as the migration owner with `runtime_role` set to
`gotth_bb_runtime`, and prove readiness before starting `app` and `caddy`.
Never put either database URL in a command argument or broad log. Existing
deployments follow the stopped upgrade and identity-rebind procedure in
`docs/release-operations.md`; they do not rerun PostgreSQL init scripts.

Do not use `docker compose down -v`. With the application stopped, the packaged
logical helper creates validated, checksummed custom archives for each database:

```sh
deploy/postgresql/backup-logical.sh \
  gotth-bb-standalone-board-postgresql-1 /absolute/backup/board.dump
deploy/postgresql/backup-logical.sh \
  gotth-bb-standalone-authentik-postgresql-1 /absolute/backup/authentik.dump
```

Prove each archive against a fresh matching-major container initialized with
the original database name and owning role. Board requires database `gotth_bb`
owned by `gotth_bb_migrate`; Authentik requires database `authentik` owned by
`authentik`. `--no-privileges` deliberately does not erase ownership. Board
restore uses the default PostgreSQL 17 contract; Authentik's pinned Alpine
database uses the explicit PostgreSQL 16 contract:

```sh
deploy/postgresql/restore-logical.sh clean-board-postgresql /absolute/backup/board.dump
deploy/postgresql/restore-logical.sh clean-authentik-postgresql /absolute/backup/authentik.dump 16
```

Before the restored Board application starts, the migration owner must apply
the packaged `deploy/postgresql/runtime-grants.sql` with
`runtime_role=gotth_bb_runtime`; a privilege-free logical archive deliberately
does not restore runtime ACLs. A recovery smoke must reuse the exact public
Board and Authentik origins and therefore the exact OIDC issuer. A different
rehearsal hostname or port is a different issuer and would create a second
forum identity instead of proving recovery.

Also retain Caddy `/data`, Authentik `/data` and `/certs`, the exact non-secret
configuration inventory, and every required secret reference. Logical database
archives alone do not preserve Caddy certificates or Authentik media/signing
state. Stop Caddy and both Authentik processes before archiving those bind
mounts. Create each filesystem archive under a root-owned mode-0700 backup
directory with `umask 077`, preserve numeric ownership, ACLs, and extended
attributes, and store a SHA-256 sidecar beside each mode-0600 archive. These are
secret-bearing recovery artifacts, not the non-secret inventory. Before
restore, verify every sidecar, require each destination bind mount to be empty,
extract with numeric ownership/ACL/xattr preservation, and rerun preflight.
Partial extraction leaves a contaminated destination that must be replaced
from the retained archive; it is never treated as a retryable clean target.
