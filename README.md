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

The repository pins Go 1.26.6, pgx v5.10.0, PostgreSQL 17.10 for integration
evidence, and sqlc v1.31.1 for local typed-query generation.

```sh
make verify
```

Apply the release's embedded forward migrations with `DATABASE_URL` already
present in the process environment:

```sh
go run -mod=readonly ./cmd/migrate
```

The migration command reads no HTTP or OIDC configuration, does not create a
connection pool, and does not provide a fake down-migration path. Do not place
database credentials directly in shell history or log the process environment.

The current HTTP shell exposes liveness and intentionally reports not-ready
until the PostgreSQL foundation supplies the required readiness checks.
