# Distribution Contract

## Endpoints

- Canonical development source:
  <https://git.dannyhunn.com/gotthboard/gotth-bb>
- Public clone and future releases:
  <https://github.com/gotthboard/gotth-bb>
- Public bug tracker:
  <https://github.com/gotthboard/gotth-bb/issues>
- Private vulnerability reports:
  <https://github.com/gotthboard/gotth-bb/security/advisories/new>

Forgejo pushes one way to GitHub. GitHub does not feed commits or tags back to
Forgejo. A ref is distributed only when the exact object ID is visible at both
endpoints.

## Maturity and compatibility

Current status: the restricted-test `1.0.0-beta.1` and
`1.0.0-beta.1.1` tags are immutable failed candidates, not known-good
releases. Live acceptance found the narrow-screen search defect recorded by
B1-06 and then a breadcrumb link-distinction defect in the corrected candidate.
The `1.0.0-beta.1.2` identity remains withheld until B1-07's guarded release
gate and owner confirmation complete.

## Current source use

Post-migration prerelease tags and the moving `main` branch are distributed at
both endpoints. Clone the repository explicitly:

```sh
git clone https://github.com/gotthboard/gotth-bb.git
```

Review the exact checked-out commit or peeled annotated tag. Do not mistake
`main`, a failed candidate, or any prerelease tag for a stable compatibility
promise.

The repository pins Go 1.26.6 where a Go module exists. Supported protocol,
runtime, database, and tool versions remain the ones stated in the README and
project verification documents; this distribution change does not widen those
contracts.

## License

The maintainer selected the MIT license for the owner-authored contents of
this repository. The standard grant is in `LICENSE`; third-party dependencies
and assets retain their own licenses. This decision does not publish a release
or create a support or compatibility promise.

## Migration traceability

| Requirement | Repository implementation | Verification |
| --- | --- | --- |
| DIST-001 | Existing history, tags, worktrees, and mirror direction remain unchanged | pinned ref and worktree inventory |
| DIST-002 | Module directive, exact self-imports, fixtures, and examples use the GitHub identity | stale-prefix search, tidy, vet, test, and clean public import |
| DIST-003/004 | README, contribution, security, changelog, and release contracts describe public use and support | documentation audit |
| DIST-006 | MIT license is present and scoped to owner-authored contents | license and dependency inventory |
| DIST-008 | Forgejo remains source and GitHub remains the one-way mirror target | push-mirror configuration and exact ref comparison |
