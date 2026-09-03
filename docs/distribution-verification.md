# GitHub Distribution Verification

Status: complete

## Identity and scope

- Pinned base: `55b989a1ba9fdf327e914fc67bb73bf8bc7f95e9`
- Publicly verified candidate: `ce473d4458009cd114a2775be7e01394aa8d935d`
- Declared module: `github.com/gotthboard/gotth-bb`
- Existing `1.0.0-alpha.1` and `1.0.0-alpha.2` tags were not changed.

Exact stale-prefix searches, `go mod tidy`, `go vet -mod=readonly ./...`, and
`go test -mod=readonly ./...` passed. On `development`, race tests repeated
50 times passed. PostgreSQL 17.10 integration tests passed in a disposable
loopback-only container.

A fresh GitHub clone of `feature/github-distribution` resolved the exact
candidate commit. Its complete `make verify` passed with task-local Node
26.7.0 and npm 12.0.2: dependency install, Templ/sqlc/Tailwind/HTMX generation,
generated-drift checks, formatting, vet, race, and package coverage. The Node
archive matched the official Node.js SHA-256 manifest. The host toolchain was
not modified.

Complete Forgejo and GitHub advertised head/tag ref sets matched after the
candidate push. Graphify recorded 2,657 nodes and 7,862 edges at implementation
commit; graph SHA-256:
`11b6adc0817ed113fe6266d3855c0afd56dce9367c71482436a40bb2f3b5827b`.
Subsequent commits before this record changed documentation only.

A fresh public GitHub `main` clone resolved
`e63ddcf26fbdec8e201035268093f570d7862edd`, produced no `go mod tidy`
drift, passed `go test -mod=readonly ./...`, and passed the complete
`make verify` gate under the exact task-local Node/npm toolchain.

Two cold Judge passes reviewed the completed candidate before promotion. This
completion update changes evidence only and receives two fresh cold passes
before commit. No performance benchmark applies because executable paths and
data flow are unchanged.

No license was selected. New release publication remains blocked. GitHub
metadata mutation lacks authentication. Forgejo is still private, so
unauthenticated public contribution and private vulnerability reporting remain
unresolved. Account conversion and ownership changes were not performed.
