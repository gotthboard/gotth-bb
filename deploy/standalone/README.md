# Standalone deployment

This directory is the version 1.0 six-service deployment boundary. It owns
Caddy, GOTTH Board, Board PostgreSQL, Authentik server/worker, and Authentik
PostgreSQL. It does not consume a host Caddy or a shared Authentik tenant.

The examples contain no working secret. Copy both example environment files to
root-owned deployment state outside the release directory, replace every
example hostname/path/image/subject, and create all required durable directories
before rendering Compose. Caddy's data and config directories must be writable
by UID/GID 65532. Secret files must be regular root-owned mode-0440 files: use
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
Then validate before starting anything:

```sh
docker compose --env-file "$GOTTH_BB_COMPOSE_ENV_FILE" \
  --project-directory deploy/standalone \
  -f deploy/standalone/compose.yml config --quiet
```

For a fresh install, start both PostgreSQL services and Authentik server/worker
first. Wait for health, then run `apply-authentik.sh` twice and require the exact
line `AUTHENTIK_BOARD_BLUEPRINT_APPLIED` both times. The import uses the mounted
OIDC secret in memory and does not write a secret-bearing blueprint.

Run the release's migration binary against the migration-role URL, apply the
packaged runtime grants as the migration owner with `runtime_role` set to
`gotth_bb_runtime`, and prove readiness before starting `app` and `caddy`.
Never put either database URL in a command argument or broad log. Existing
deployments follow the stopped upgrade and identity-rebind procedure in
`docs/release-operations.md`; they do not rerun PostgreSQL init scripts.

Do not use `docker compose down -v`. Backup and clean-restore both PostgreSQL
databases and retain Caddy `/data`, Authentik `/data`, `/certs`, the exact
non-secret configuration inventory, and every required secret reference.
