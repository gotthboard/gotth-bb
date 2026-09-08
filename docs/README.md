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

Implementation history is recorded in the [change log](CHANGELOG.md).

The current target is `1.0.0-alpha.N`: AN-03 unread state is merged and the
AN-04 administration-completion contract is admitted for serial implementation.
AN-05 abuse controls, beta, release-candidate, and stable admission remain
subsequent boundaries before `1.0.0`.

## Document status

| Document | Status | Governs |
| --- | --- | --- |
| Product requirements | Draft | User-visible behavior and release scope |
| Architecture | Draft | System boundaries and technical decisions |
| Implementation specification | Draft | Concrete contracts and code organization |
| Feature plan | Draft | Dependency order and delivery milestones |
| Verification | Draft | Evidence required for acceptance |
| Release and operations | Draft | Deployment, rollback, and runtime operation |
