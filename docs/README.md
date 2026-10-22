# Project documentation

This directory is the canonical home for project documentation. Documents are
ordered by dependency; a downstream document may clarify an upstream decision,
but it may not silently change one.

1. [Product requirements](prd.md)
2. [Architecture](architecture.md)
3. [Implementation specification](implementation-spec.md)
4. [Feature decomposition and delivery plan](feature-plan.md)
5. [Traceability and verification](verification.md)
6. [Release and operations](release-operations.md)

Beta.1 supporting inventories are [requirement traceability](beta1-traceability.md)
and the [route and authority inventory](beta1-routes.md). They are verified
against the governing PRD and production source; they do not create runtime
policy or route registration.

Implementation history is recorded in the [change log](CHANGELOG.md).

The latest tagged and deployed restricted-test candidate is
`1.0.0-beta.1.9`. Its tag, artifact, deployment, restart, and smoke evidence is
complete. All twelve tag fallback URLs exist, but GitHub has no published
Release objects. Release closeout must create prerelease records; Beta.1.9 alone receives the
exact archive/checksum assets. Owner physical publication acceptance remains
pending, so it is not yet known-good. RC.1 and stable
admission have not occurred.

B1-10 explicitly requires nested categories. Its contract is planned, not
implemented; [the feature plan](feature-plan.md#b1-10-nested-discussion-areas)
and blocked traceability rows distinguish this scope from the current flat-
category behavior.

## Document status

| Document | Status | Governs |
| --- | --- | --- |
| Product requirements | Draft | User-visible behavior and release scope |
| Architecture | Draft | System boundaries and technical decisions |
| Implementation specification | Draft | Concrete contracts and code organization |
| Feature plan | Draft | Dependency order and delivery milestones |
| Verification | Draft | Evidence required for acceptance |
| Release and operations | Draft | Deployment, rollback, and runtime operation |
