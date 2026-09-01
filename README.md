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

The repository pins Go 1.26.6. The current foundation uses only the Go standard
library; later dependencies remain unselected until their current contracts are
reviewed.

```sh
make verify
```

The current HTTP shell exposes liveness and intentionally reports not-ready
until the PostgreSQL foundation supplies the required readiness checks.
