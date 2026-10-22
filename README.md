# GOTTH Board

> **Distribution:** GitHub is the public clone and future release endpoint.
> Forgejo remains canonical development. Report public bugs through GitHub
> Issues and security vulnerabilities through GitHub private reporting.
> See [the distribution contract](docs/distribution.md).


A self-hosted bulletin board and forum built with the GOTTH stack and
PostgreSQL.

The initial development deployment target is
`https://bb.alhstudios.com/`. That temporary URL is not the product identity.

The planned stack is Go, Templ, HTMX, Tailwind CSS, and PostgreSQL, with
Authentik as the identity provider.

The latest tagged and deployed restricted-test candidate is
`1.0.0-beta.1.9`. Automated tag, artifact, deployment, restart, and smoke
evidence is recorded. All twelve tag fallback URLs exist, but GitHub has no
published Release objects. Release closeout must create prerelease records; Beta.1.9 alone receives the
exact archive/checksum assets. Owner physical acceptance of the real
authenticated topic-publication workflow remains pending.
The candidate is therefore not yet a known-good rollback reference and does not
claim RC.1 or stable status.

Nested categories are now required for version 1.0 under planned **B1-10**:
arbitrary finite depth, inherited restrictions, stable URLs, and safe subtree moves.
They are not implemented by this documentation update and remain a release
scope gap. See [the hierarchy plan](docs/feature-plan.md#b1-10-nested-discussion-areas).

Project documentation lives in [`docs/`](docs/README.md).

## Development

The repository pins Go 1.26.6, Node.js 26.7.0, npm 12.0.2, pgx v5.10.0,
PostgreSQL 17.10 for integration evidence, sqlc v1.31.1, templ v0.3.1020,
Chi v5.3.2, Tailwind CSS v4.3.3, and HTMX v2.0.10.

```sh
make verify
```

`make verify` installs the exact frontend lock with package scripts disabled,
regenerates Templ, sqlc, Tailwind, and the vendored HTMX asset, rejects generated
drift, then runs formatting, vet, race, and coverage tests. Node and npm must
match `.node-version` and `package.json`; no global templ, sqlc, or Tailwind
installation is used.

A release archive is built only from a clean checkout of the exact native
tagged commit. The target directory must not already exist; packaging does not
overwrite prior evidence.

```sh
RELEASE_VERSION=1.0.0-beta.1.9 \
RELEASE_COMMIT="$(git rev-parse '1.0.0-beta.1.9^{commit}')" \
RELEASE_GOOS="$(go env GOOS)" \
RELEASE_GOARCH="$(go env GOARCH)" \
RELEASE_OUTPUT="$PWD/dist/1.0.0-beta.1.9" \
make release
```

`make release` first runs the full verification contract, then builds the
forum, migration, and operator executables with the same linker identity. It
executes the migration and operator binaries' database-free `version` checks
and emits one normalized `.tar.gz` plus `SHA256SUMS`. The archive also contains
exact release metadata, the read-only Go module dependency manifest, the
runtime PostgreSQL grant contract, and the application container
build/entrypoint/Compose files from that exact commit. Release packaging is
native only so both executable identity checks run before the artifact is
admitted.

## Container runtime

The current restricted-test deployment uses the release's standalone Compose
model. It keeps seven long-running services separate: stack Caddy, GOTTH Board,
the Authentik control gateway, Board PostgreSQL 17, Authentik PostgreSQL 16,
Authentik server, and Authentik worker. They are deliberately not combined.
The older two-service `deploy/container/compose.yml` remains package history,
not the description of the current deployed topology.

The application uses the host network namespace but still binds only
`127.0.0.1:18082`, runs as UID/GID 65532 with a read-only root filesystem,
drops every Linux capability, and sends logs to journald. Caddy remains the
public TLS boundary. PostgreSQL retains its durable host bind at
`/tank/gotth-bb/postgres17` and its loopback maintenance port. Host networking
preserves the application's production loopback-only security contract and the
same host-local network reachability as the previous native process.

Compose interpolation supplies the image name and host file paths. Database,
OIDC, and activity-cursor keyring secret values live in separate root-managed
files mounted under `/run/secrets`; they do not belong in the image, Compose
file, or Docker image configuration. The application receives only the
keyring's non-secret mount path through `ACTIVITY_CURSOR_KEYRING_FILE`.
AN-05's non-secret canonical abuse rules are mounted separately and read-only
at `/run/config/gotth-bb-abuse-rules`; only that path enters the environment.
See [`docs/release-operations.md`](docs/release-operations.md) for the deployment
and rollback contract.

Apply the release's embedded forward migrations with `DATABASE_URL` already
present in the process environment:

```sh
go run -mod=readonly ./cmd/migrate
```

The migration owner must then grant the runtime role only the column-level
privilege PostgreSQL requires for the governance singleton lock:

```sh
psql --set=ON_ERROR_STOP=1 --set=runtime_role=gotth_bb_runtime \
  --file=deploy/postgresql/runtime-grants.sql "$DATABASE_URL"
```

The grant is idempotent. It does not give the runtime role table-wide UPDATE or
DELETE access to `governance_state`.

The migration command reads no HTTP or OIDC configuration, does not create a
connection pool, and does not provide a fake down-migration path. Do not place
database credentials directly in shell history or log the process environment.
Its release identity can be inspected without loading database configuration:

```sh
gotth-bb-migrate version
```

After the intended administrator has completed one successful Authentik login
and therefore has an existing local `(issuer, subject)` identity, grant the
first local administrator role with the separate one-shot operator command:

```sh
go run -mod=readonly ./cmd/operator bootstrap-administrator \
  --issuer 'https://auth.example/application/o/gotth-bb/' \
  --subject 'exact-provider-subject' \
  --operator 'operator-audit-identifier'
```

The command reads only `DATABASE_URL`, prints only committed user/audit IDs,
and rejects missing, duplicate, unknown, or extra arguments. It serializes the
zero-administrator decision and commits the role plus immutable operator audit
event together. Do not retry an unknown result blindly; inspect the user and
audit state first. Later attempts fail once an active administrator exists.

The HTTP shell renders base-path-safe forum pages, serves versioned embedded
assets, exposes liveness, and reports ready only when the live database matches
the embedded migration release, contains exactly one governance singleton, and
has at least one active administrator.

## Installation, compatibility, and support

The latest tagged and deployed application candidate is `1.0.0-beta.1.9`.
All earlier tags remain immutable, including failed or superseded Beta
candidates. Owner physical acceptance is still pending, B1-10 nested categories
remain planned rather than implemented, and neither this prerelease nor `main`
is a stable compatibility promise. To inspect the current source:

```sh
git clone https://github.com/gotthboard/gotth-bb.git
```

The owner-authored repository contents are licensed under the
[MIT License](LICENSE). Third-party dependencies and assets retain their own
licenses. No long-term support promise is implied. Versioning, release
admission, security reporting, and contribution details are in
[the release policy](docs/RELEASING.md), [security policy](SECURITY.md), and
[contribution guide](CONTRIBUTING.md).
