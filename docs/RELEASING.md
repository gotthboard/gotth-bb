# Release Policy

`gotth-bb` versions independently under Semantic Versioning. A version number in
another GOTTH repository creates no release obligation here.

If this repository contains a Go module, release tags use the Go-compatible
`vMAJOR.MINOR.PATCH` form, including any SemVer prerelease suffix. Historical
tags are immutable even when they predate this convention.

No release may be tagged while the repository lacks an explicit license or
while required verification is incomplete. Reserved placeholders are not
tagged at all.

For an admitted release:

1. Select a version from this repository's actual compatibility change.
2. Verify an exact clean Forgejo commit with the repository's required gates.
3. Update `docs/CHANGELOG.md` with the exact version, commit, compatibility
   effect, verification, and known limitations.
4. Create and push a new annotated tag on canonical Forgejo. Never move or
   replace an existing tag.
5. Verify exact head and tag parity at
   `github.com/gotthboard/gotth-bb` through the existing one-way push mirror.
6. Build any artifacts from the exact public GitHub clone, publish checksums,
   and create the GitHub release for that already-mirrored tag.

Forgejo remains the development source of truth. GitHub is the public clone,
Go import, and release-distribution endpoint. Failure of mirror or release
publication blocks the release; it does not authorize reversing mirror
direction or publishing a different commit.
