# GOTTH Board

A self-hosted bulletin board and forum built with the GOTTH stack and
PostgreSQL.

The initial development deployment target is
`https://alhstudios.com/bb/`. That temporary URL is not the product identity.

The planned stack is Go, Templ, HTMX, Tailwind CSS, and PostgreSQL, with
Authentik as the identity provider.

Implementation of `1.0.0-alpha.1` has begun. No application release exists
yet.

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

Apply the release's embedded forward migrations with `DATABASE_URL` already
present in the process environment:

```sh
go run -mod=readonly ./cmd/migrate
```

The migration command reads no HTTP or OIDC configuration, does not create a
connection pool, and does not provide a fake down-migration path. Do not place
database credentials directly in shell history or log the process environment.

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

The current HTTP shell renders the base-path-safe public area placeholder,
serves versioned embedded assets, exposes liveness, and intentionally reports
not-ready until migration-head and administrator invariants are wired into the
readiness check.
