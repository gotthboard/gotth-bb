# Immutable prerelease status manifest

Every GitHub Release body created during closeout must contain the exact marker
shown here. The marker records history; it does not retroactively admit a failed
or superseded candidate.

| Tag | Required body marker | Custom assets |
| --- | --- | --- |
| `1.0.0-alpha.1` | `GOTTH-Release-Status: historical-alpha` | none |
| `1.0.0-alpha.2` | `GOTTH-Release-Status: historical-alpha` | none |
| `1.0.0-beta.1` | `GOTTH-Release-Status: failed-search-overflow` | none |
| `1.0.0-beta.1.1` | `GOTTH-Release-Status: failed-breadcrumb-accessibility` | none |
| `1.0.0-beta.1.2` | `GOTTH-Release-Status: rejected-auth-topology` | none |
| `1.0.0-beta.1.3` | `GOTTH-Release-Status: superseded-footer-defect` | none |
| `1.0.0-beta.1.4` | `GOTTH-Release-Status: superseded-area-ui-defect` | none |
| `1.0.0-beta.1.5` | `GOTTH-Release-Status: owner-acceptance-unrecorded` | none |
| `1.0.0-beta.1.6` | `GOTTH-Release-Status: failed-register-404` | none |
| `1.0.0-beta.1.7` | `GOTTH-Release-Status: failed-smtp-administration` | none |
| `1.0.0-beta.1.8` | `GOTTH-Release-Status: failed-restricted-role-publication` | none |
| `1.0.0-beta.1.9` | `GOTTH-Release-Status: candidate-owner-acceptance-pending` | exact archive + `SHA256SUMS` |

All twelve objects are prereleases. Beta.1.9 remains not known-good until owner
physical acceptance; B1-10, RC.1, stable, and support commitments remain open.
