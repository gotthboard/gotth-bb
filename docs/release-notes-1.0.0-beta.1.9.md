# GOTTH Board 1.0.0-beta.1.9

GOTTH-Release-Status: candidate-owner-acceptance-pending

Restricted-test prerelease candidate correcting topic publication under the
least-privilege runtime role.

## Status

- Annotated tag object: `a9e59395a9c522f92c1594364456a66c5cd6f202`
- Peeled commit: `ba23abd4c3bc825efafd31d56c671c15a31bf902`
- Source tree: `2f26e91e542ad59c0df032d09d9feff6b9020dc3`
- Release archive SHA-256:
  `a6c611e1f3d402fe4eab460fda7864eb14669f2a98e628cde0884d724ce99690`

A fresh exact-tag `make release` run on 2026-10-21 reproduced the archive checksum above. The candidate passed the recorded restricted-role PostgreSQL regression,
complete Go verification, two public-tag clean builds, two clean reviews,
application-only deployment, restart, and smoke checks. It remains a
**prerelease candidate**: owner physical acceptance of the real authenticated
topic-publication workflow is still pending. It is not a known-good rollback
reference, RC.1, or stable release. B1-10 nested categories remain planned.

The correction removes a redundant mapping-row lock that required an UPDATE
grant the runtime deliberately does not have. Parent-area serialization and the
least-privilege SELECT/INSERT/DELETE contract remain intact. No schema, data,
grant, identity, Caddy, SMTP, or configuration change is included.
