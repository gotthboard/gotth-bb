# Standalone deployment

This directory is the version 1.0 six-service deployment boundary. It owns
Caddy, GOTTH Board, Board PostgreSQL, Authentik server/worker, and Authentik
PostgreSQL. It never shares Caddy configuration/state or an Authentik tenant.
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
keyring. The pinned Authentik server receives supplemental groups 999 and
65532; the worker receives only 999. These numeric identities are part of the
pinned-image verification gate, not guesses about mutable tags. The Board
PostgreSQL directory must not overlap the Authentik PostgreSQL directory.
Every secret file contains the exact nonempty secret bytes with no trailing
newline, carriage return, or other framing; Authentik's documented `file://`
loader treats those bytes literally.

The Board database URL secret names `gotth_bb_runtime` and the loopback
maintenance port. Its password must equal the Board runtime password secret.
The migration URL is operator input and is never stored in Compose. The OIDC
client secret file is shared only by the Board application and Authentik server;
the worker does not receive it. The pinned Authentik worker does not honor the
documented `file://` password form consistently in its Rust database path. The
packaged non-root entrypoint therefore reads the two mounted Authentik secrets,
validates their framing, exports them only into the Authentik process, and
replaces itself. Docker image/configuration metadata retains no secret value.

Set `GOTTH_BB_COMPOSE_ENV_FILE` to the absolute deployment-environment path.
Run the root-only preflight before starting anything. It rejects mismatched
public/OIDC origins, overlapping durable paths, incorrect numeric ownership or
modes, secret framing, mutable Board image naming, and an invalid Compose
render:

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
addresses, `GOTTH_BB_CADDY_BIND=0.0.0.0`, and
`GOTTH_BB_CADDY_CLIENT_ADDRESS={remote_host}`. A shared-host deployment uses
the same public HTTPS origins, `http://HOST:PORT` Caddy addresses,
`GOTTH_BB_CADDY_BIND=127.0.0.1`, and
`GOTTH_BB_CADDY_CLIENT_ADDRESS={http.request.header.X-Forwarded-For}`. The
outer TLS edge must overwrite `X-Forwarded-For` with exactly `{remote_host}`
and remove `Forwarded` and `X-Real-IP`; it must preserve the original `Host`.
The inner Caddy repeats alternate-header removal and supplies the one canonical
address to Board. Arbitrary proxy chains are not admitted.

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

Prove each archive against a fresh matching-major container. Board restore uses
the default PostgreSQL 17 contract; Authentik's pinned Alpine database uses the
explicit PostgreSQL 16 contract:

```sh
deploy/postgresql/restore-logical.sh clean-board-postgresql /absolute/backup/board.dump
deploy/postgresql/restore-logical.sh clean-authentik-postgresql /absolute/backup/authentik.dump 16
```

Also retain Caddy `/data`, Authentik `/data` and `/certs`, the exact non-secret
configuration inventory, and every required secret reference. Logical database
archives alone do not preserve Caddy certificates or Authentik media/signing
state.
