# Security Policy

## Supported versions

1.0.0-alpha.2 application; existing tags predate the GitHub module identity.
No version currently carries a separate long-term security-support promise.
Once releases exist, supported
versions will be listed here and in the changelog.

## Search and activity boundaries

AN-02 discovery is authorization-first. Restricted rows must not contribute a
search identity, field, count, rank, excerpt, activity cursor, or page
terminality before the same area/topic predicate used by direct reads succeeds.
Group access is a server-owned `EXISTS` predicate, not client authority or a
row-producing join. Search/activity responses are private and not cached.

Post excerpts and vectors derive only from the admitted sanitizer's visible
text nodes. Raw Markdown, removed HTML, attributes, URL destinations, comments,
and other sanitizer-discarded input are not discovery data. A stale projection
fails discovery closed but cannot hide an otherwise authorized direct post.

Activity cursors contain a bounded boundary and access-snapshot digest, not
content. They are HMAC-authenticated under an external read-only keyring, expire
after 24 hours using PostgreSQL time, and are invalidated by role/group changes.
Cursor key bytes are excluded from repository, process arguments, environment,
logs, metrics, evidence, and release records. Key expiry degrades only activity;
it does not weaken authorization or close unrelated routes.

Application logs exclude discovery filters, cursors, identities, result fields,
and snippets. The edge still receives the raw query-bearing URL, and explicitly
enabled PostgreSQL statement/parameter diagnostics may see bound values. Those
separate operator-controlled systems require appropriate access and retention.

## Reporting a vulnerability

Do not disclose exploit details in a public GitHub issue, pull request,
discussion, or Forgejo ticket. Report vulnerabilities privately through
GitHub's private vulnerability-reporting form:

<https://github.com/gotthboard/gotth-bb/security/advisories/new>

Include the affected version or commit, impact, reproduction steps, and any
suggested remediation when available. The maintainers will acknowledge the
report and coordinate validation, remediation, and disclosure through the
private advisory.

Use GitHub Issues only for non-sensitive bugs. If the private form cannot be
used, open a GitHub issue containing no vulnerability details and ask the
maintainers to restore private reporting access.
