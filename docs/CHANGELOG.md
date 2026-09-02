# Change log

This file records admitted implementation changes. Release notes remain a
separate artifact governed by the release and operations plan.

## Unreleased

### 2026-09-01 23:48 CDT — Serialize authorized topic and reply publication

Commit: current commit; hash assigned by Git after commit

Affected files:

- `docs/implementation-spec.md`
- `internal/forum/publish.go`
- `internal/forum/publish_integration_test.go`
- `internal/forum/publish_test.go`
- `internal/policy/reply.go`
- `internal/policy/reply_test.go`
- `internal/store/db/publishing.sql.go`
- `internal/store/db/publishing_test.go`
- `internal/store/queries/publishing.sql`

Explanation:

Added the first A1-07 publishing service and PostgreSQL boundary. Topic
creation validates a bounded control-free title, renders the first Markdown
post, locks the current area/group policy, and commits the topic plus first
post as one deferred-integrity transaction. Reply creation renders before its
transaction, locks the current topic/area/group policy, applies the closed
reply policy, then inserts the post and advances all topic counters/activity in
one statement.

Members publish only in visible normal areas and open topics. Moderator and
administrator staff may publish in read-only areas and locked/hidden topics;
archived areas or topics reject everyone. Suspended, muted, anonymous,
malformed, mismatched-group, unknown-state, deleted-topic, and denied actors
fail closed. Ordinary publication does not fabricate moderation audit rows.

The topic row lock serializes `next_post_number`. Reply timestamps use the
greater of the supplied bounded UTC time and existing topic activity so a
request that waited behind a newer reply cannot move post chronology or topic
activity backward.

Verification:

- Reply policy has 100% statement coverage across roles, visibility, posting
  modes, topic states, suspension, mute, group membership, and malformed input
- Every generated publishing binding has 100% statement coverage; the service
  boundary has 98%, with only impossible opaque-render persistence failures
  uncovered
- PostgreSQL 17 proves one topic/first-post commit, read-only denial without a
  row, eight concurrent unique contiguous replies, exact counters/latest post,
  renderer persistence, and nondecreasing timestamps under a decreasing clock

Risks / non-goals:

- This unit does not add browser forms, preview, edit/delete, or rate limiting;
  it establishes the transaction and authorization boundary those routes call
- Publication performs bounded rendering plus three statements inside one
  transaction; begin and commit add the two transaction-boundary round trips.
  It never retries an unknown commit outcome

### 2026-09-01 23:05 CDT — Render bounded sanitized Markdown

Commit: current commit; hash assigned by Git after commit

Affected files:

- `docs/implementation-spec.md`
- `go.mod`
- `go.sum`
- `internal/render/markdown.go`
- `internal/render/markdown_test.go`

Explanation:

Added the fixed A1-07 Markdown rendering boundary. It accepts only nonblank
UTF-8 source from 1 through the schema's 65,536-byte maximum, renders plain
CommonMark with Goldmark v1.8.5, preserves Goldmark's default raw-HTML and
dangerous-link protections, and then applies the existing narrow Bluemonday
policy. Sanitized output must remain nonblank and within the schema's 262,144-
byte rendered limit.

The private `RenderedMarkdown` type cannot be forged outside the render
package. Its zero value renders empty but cannot produce persistence values; a
valid value returns sanitized HTML together with the exact immutable renderer
version `goldmark-v1.8.5-bluemonday-v1.0.27-p1`, or converts without allocation
to the existing opaque `TrustedHTML` presentation type. Plain CommonMark is
deliberate: no tables, task lists, heading IDs, automatic linkifier, raw HTML,
or runtime extension was admitted.

Verification:

- Paragraphs, emphasis, strong text, ordered/unordered lists, block quotes,
  inline/fenced code, links, and basic emoji survive the documented pipeline
- Raw HTML remains non-executable, JavaScript/data links lose their links, and
  safe text is preserved; every surviving relative/HTTP/HTTPS link receives
  the fixed `nofollow noreferrer` policy
- Empty, whitespace-only, invalid UTF-8, source-overflow, raw-HTML-only, and
  rendered-output-overflow inputs fail before persistence
- Maximum-size source, zero-value safety, persistence/version pairing,
  presentation equivalence, and 64 concurrent conversions pass under the race
  detector
- Full `make verify` passes. Persistence, trusted conversion, and validity
  functions have 100% statement coverage; RenderMarkdown is 88.9%

Risks / non-goals:

- The sole uncovered RenderMarkdown branch handles Goldmark returning an error
  while writing to `bytes.Buffer`. The real plain-CommonMark renderer and an
  in-memory buffer provide no injectable failure; replacing either solely to
  manufacture coverage would add fake abstraction
- The renderer is not yet wired to topic/reply forms or transactions. This unit
  establishes the exact persistence values those write paths must consume
- Rendering and sanitization are linear in bounded source/intermediate/output
  bytes and allocate the Goldmark AST plus render and sanitizer buffers. No
  I/O, retry, cache mutation, runtime extension, or detached work occurs

### 2026-09-01 22:50 CDT — Render bounded visible topic post pages

Commit: current commit; hash assigned by Git after commit

Affected files:

- `cmd/forum/main.go`
- `cmd/forum/main_test.go`
- `docs/implementation-spec.md`
- `internal/httpui/authenticated_handler.go`
- `internal/httpui/authenticated_handler_test.go`
- `internal/httpui/handler.go`
- `internal/httpui/handler_test.go`
- `internal/httpui/shell.templ`
- `internal/httpui/shell_templ.go`
- `internal/httpui/static/app-1.0.0-alpha.1.css`
- `internal/httpui/static_test.go`
- `internal/httpui/topic_id.go`
- `internal/httpui/topic_id_test.go`
- `internal/httpui/topic_post_handler.go`
- `internal/httpui/topic_post_handler_test.go`
- `internal/httpui/url_builder.go`
- `internal/httpui/url_builder_test.go`
- `internal/httpui/view.go`

Explanation:

Added the ordinary `GET /topics/{id}` read path from process composition through
the exact session boundary, Chi route, bounded access-aware store call, narrow
presentation projection, trusted-HTML conversion, and full/HTMX templates.
Topic IDs accept only canonical positive decimal `int64` spelling; post pages
reuse the fixed 1-10,000 query contract and 25-row store bound. Missing,
inaccessible, malformed, and empty-later results preserve the generic `404` or
redacted `503` boundary without exposing persistence details.

Topic pages render access-filtered breadcrumbs and counts, pinned/open/locked/
hidden/archived state, authorship and UTC timestamps, sanitized persisted post
bodies, stable `#post-<id>` anchors, canonical later-page URLs, and bounded
previous/next navigation. One URL-builder method owns path, deterministic query,
and escaped fragment assembly. Noncanonical escaped topic paths and
noncanonical numeric IDs bypass session lookup, so they remain public `404`
responses even when the session store is unavailable.

The forum process now wires `store.GetVisibleTopicPostPage` through the same
generated query set as area reads. Its lifecycle test executes the generated
query binding end to end, proves anonymous authority arguments, and renders the
sanitized returned body through the live server.

Verification:

- Full and HTMX responses, page-1/later canonical URLs, stable post links,
  previous/next navigation, empty topics, every visible topic state, edited and
  unedited metadata, hostile HTML, unsafe link schemes, generic missing results,
  redacted failures, malformed rows/sentinels, and committed write failures are
  covered under the race detector
- Router tests prove exact route patterns, canonical identifier/page binding,
  anonymous and authenticated authority propagation, and no session lookup for
  malformed, nested, escaped, wrong-method, or noncanonical topic paths
- Process-level HTTP testing proves generated PostgreSQL argument order and the
  rendered trusted-HTML result; the pinned Tailwind asset digest was updated to
  `4bb5a0a324f4cdddfa8358a9dd3f3a07a125977ff1194718363d4d03c0ac43c1`
- Full `make verify` passes. Canonical ID parsing, fragment URL construction,
  and metadata comparison have 100% statement coverage; the new topic handler
  has 97.7%
- Cold review removed an unsafe `int64`-to-`int32` total-page narrowing and
  required the authorized-empty sentinel's repeated count to agree with the
  page total

Risks / non-goals:

- The handler's sole uncovered two-statement branch retains fail-closed error
  handling if permalink construction fails after the same immutable builder,
  fixed positive path segments, and non-empty generated fragment have already
  succeeded. That state is mechanically unreachable without corrupting captured
  handler state; removing the check to manufacture 100% would be dishonest
- This ordinary read route excludes soft-deleted posts and does not add reply,
  edit, read-marker, moderation, or Markdown-writing behavior
- At most 25 schema-bounded bodies are re-sanitized and buffered per page. The
  work is linear in returned rows and HTML bytes, with one database query, no
  retry, no detached work, and no hidden cache

### 2026-09-01 22:25 CDT — Enforce trusted rendered HTML boundary

Commit: current commit; hash assigned by Git after commit

Affected files:

- `go.mod`
- `go.sum`
- `internal/render/trusted_html.go`
- `internal/render/trusted_html_test.go`
- `docs/CHANGELOG.md`

Explanation:

Added the sole persisted-content boundary permitted to bypass Templ escaping.
Its opaque `TrustedHTML` type has a private representation and safe zero value;
the only constructor always runs a fixed Bluemonday v1.0.27 policy before the
type can produce a Templ component. The boundary sanitizes again at read time,
so a corrupt row or obsolete Markdown renderer cannot inject active markup.

The allowlist contains only paragraphs, emphasis, strong emphasis, ordered and
unordered lists, list items, links, block quotes, preformatted/code text, and
line breaks. Styling, identifiers, event attributes, images, tables, embeds,
scripts, and arbitrary metadata are stripped. Only parseable relative, HTTP,
and HTTPS links survive; every surviving link receives `nofollow noreferrer`.
Applying that relation policy to local links too deliberately removes the seam
between Go and browser classification of ambiguous relative references.

Verification:

- Intended formatting and emoji survive while executable, embedded,
  undocumented, styled, and attribute-bearing markup is removed
- JavaScript, data, and mail schemes lose their links; local, scheme-relative,
  ambiguous-relative, HTTP, and HTTPS references retain only the fixed policy
- Component rendering emits sanitized markup without a raw-string accessor;
  the zero value renders empty
- The shared completed policy passed concurrent race testing, 100 repeated
  race-detector runs, full `make verify`, and 100% statement coverage for every
  changed production function
- Cold review found and removed the external-only URL-classification seam;
  two fresh reviews then returned CLEAN on the admitted code state

Risks / non-goals:

- This unit does not parse Markdown or wire the topic HTTP handler; it defines
  the presentation trust boundary those later units must consume
- Sanitization allocates output linear in the schema-bounded rendered HTML;
  it performs no I/O, retry, detached work, or cache mutation
- Bluemonday adds two transitive CSS-policy modules even though this narrow
  policy allows no style attributes; replacing a reviewed sanitizer with a
  hand-written HTML parser would be a worse security trade

### 2026-09-01 22:06 CDT — Bound visible topic post pages

Commit: current commit; hash assigned by Git after commit

Affected files:

- `internal/store/topic_post_pages.go`
- `internal/store/topic_post_pages_test.go`
- `docs/CHANGELOG.md`

Explanation:

Added the store boundary that owns topic/post pagination and authority
derivation. It accepts only a positive topic ID, pages 1 through 10,000, and a
canonical access snapshot; derives staff/member/group query parameters; fixes
the limit at 25; and caps the offset at 249,975. Missing topics, invalid input,
and empty later pages retain the same no-row result.

The boundary validates every repeated topic/breadcrumb field, exact window
total, expected page length, strict post-number ordering, positive stable IDs,
required renderer/author/revision fields, finite ordered timestamps, and the
all-null authorized-empty sentinel before presentation receives persisted HTML.
The generated row slice and group slice are reused without copies.

Verification:

- Visitor, member, moderator, and administrator inputs derive only the intended
  SQL authority facts and preserve exact group IDs
- Page 2, the maximum page/offset, authorized empty page 1, invalid IDs/pages,
  empty later pages, cancellation, malformed authority, and query failures are
  covered
- Oversized, incomplete, inconsistent, unordered, nonfinite, nullable, unknown-
  state, hidden-without-staff, and malformed-sentinel row sets fail closed
- Every production function in this unit has 100% statement coverage under the
  race detector

Risks / non-goals:

- Persisted rendered HTML is structurally validated but is not yet converted to
  the explicit trusted-HTML presentation type; that remains the next read unit
- The store deliberately does not retry PostgreSQL failures or copy result rows

### 2026-09-01 21:55 CDT — Query atomic visible topic post pages

Commit: current commit; hash assigned by Git after commit

Affected files:

- `internal/store/queries/topic_posts.sql`
- `internal/store/db/topic_posts.sql.go`
- `internal/store/db/topic_posts_test.go`
- `internal/store/db/topics_integration_test.go`
- `docs/CHANGELOG.md`

Explanation:

Added the generated PostgreSQL query for one authorized topic and one bounded
page of its nondeleted posts. Topic text, owning-area breadcrumb fields, post
content, author labels, and the exact visible-post count share one statement and
snapshot. A left join emits one nullable post row only when page 1 addresses a
visible topic with no visible posts; applying a later offset removes that
sentinel and yields the normal missing-page result.

The complete area predicate is evaluated before topic fields leave PostgreSQL.
Hidden topics require staff authority, deleted topics remain absent for every
ordinary reader, deleted posts never enter rows or counts, and group IDs have no
effect without verified member authority.

Verification:

- Generated binding tests preserve context, exact typed parameter order, all
  nullable scan fields, close behavior, and query/scan/rows failures
- PostgreSQL 17 proves first/second/empty-later pages, ascending immutable post
  order, window totals, soft-deleted post exclusion, and the authorized-empty
  page-1 sentinel
- PostgreSQL 17 covers visitor, member, matching/nonmatching group, moderator
  authority, hidden/deleted/missing topics, and attempted group injection

Risks / non-goals:

- The generated method accepts raw pagination values; the store boundary will
  derive and enforce the fixed 25-row limit and maximum offset
- Rendering persisted sanitized HTML and the HTTP topic route remain later
  A1-06 units

### 2026-09-01 21:52 CDT — Specify bounded topic read pages

Commit: current commit; hash assigned by Git after commit

Affected files:

- `docs/implementation-spec.md`
- `docs/feature-plan.md`
- `docs/CHANGELOG.md`

Explanation:

Closed the remaining A1-06 implementation choices before adding topic-detail
SQL or handlers. Topic IDs now have one canonical positive decimal contract;
post pages use fixed 25-row ascending pages with a 10,000-page ceiling; empty
later pages collapse into the normal inaccessible-topic `404`; and stable post
URLs use the topic ID, canonical page query, and post ID fragment.

The contract requires topic metadata, posts, counts, and breadcrumbs to come
from one access-filtered PostgreSQL statement and snapshot. A left-joined null
post row represents an authorized topic with no visible posts on page 1; later
offsets receive no sentinel and collapse to `404`. This avoids mixed-authority
results during concurrent visibility changes. Ordinary topic reads exclude
soft-deleted posts rather than exposing moderation-retained content.

Verification:

- Route, identifier, pagination, ordering, URL, and soft-deletion behavior are
  explicit enough to constrain SQL, store, and HTTP tests
- Maximum page offset is fixed at 249,975 and only a fixed 25-row limit reaches
  PostgreSQL
- The single-snapshot query shape forbids mixed-authority metadata/posts during
  concurrent policy changes

Risks / non-goals:

- This contract does not yet implement the topic queries or page
- Staff moderation views of deleted posts remain part of A1-08, not the ordinary
  topic read path

### 2026-09-01 21:40 CDT — Link visible discussion areas

Commit: current commit; hash assigned by Git after commit

Affected files:

- `internal/policy/area_slug.go`
- `internal/policy/area_slug_test.go`
- `internal/store/areas.go`
- `internal/httpui/area_index_handler.go`
- `internal/httpui/area_index_handler_test.go`
- `internal/httpui/handler.go`
- `internal/httpui/view.go`
- `internal/httpui/shell.templ`
- `internal/httpui/shell_templ.go`
- `docs/CHANGELOG.md`

Explanation:

Turned every access-filtered area name on the community index into a canonical,
prefix-aware link to `GET /areas/{slug}` with progressive HTMX navigation. The
database slug grammar now has one allocation-free domain validator shared by
storage lookup and browser projection, preventing schema-invalid rows from
becoming dead or ambiguous links. Malformed rows discard the complete result
and return the same redacted unavailable page as a store failure.

Verification:

- Complete and HTMX index responses render exact `/bb/areas/<slug>` `href` and
  `hx-get` attributes with main-content replacement and history updates
- Area name and description escaping remains intact; hidden visibility and
  posting-mode fields remain absent
- Empty results, store failures, invalid slugs, missing names, missing listers,
  invalid URL builders, and committed-write failures retain fail-closed behavior
- The shared validator accepts the schema maximum and canonical lowercase ASCII
  grammar while rejecting overflow, separators, Unicode, controls, spaces,
  uppercase, and malformed hyphens

Risks / non-goals:

- Topic links currently lead to the admitted topic-list route; topic detail
  pages and all publishing operations remain separate alpha units
- This change does not alter area visibility, ordering, or administration

### 2026-09-01 21:26 CDT — Wire visible area topic routes

Commit: current commit; hash assigned by Git after commit

Affected files:

- `cmd/forum/main.go`
- `internal/httpui/handler.go`
- `internal/httpui/handler_test.go`
- `internal/httpui/area_topic_handler.go`
- `internal/httpui/area_topic_handler_test.go`
- `internal/httpui/authenticated_handler.go`
- `internal/httpui/authenticated_handler_test.go`
- `docs/CHANGELOG.md`

Explanation:

Activated `GET /areas/{slug}` in the application router and connected it to the
access-filtered PostgreSQL topic-page store. Exact one-segment area requests now
pass the complete server-owned session access context, including group IDs, to
the topic loader. Malformed area paths, nested paths, wrong methods,
infrastructure routes, unknown routes, and escaped non-canonical paths remain
outside the session and topic-store lookup boundaries so unavailable identity
or persistence state cannot break unrelated public HTTP behavior.

Verification:

- Public routing binds the exact slug and route pattern and supplies visitor
  authority to the topic loader
- Authenticated routing preserves member identity, role, and group IDs through
  the session boundary and renders the selected area
- Missing, empty, nested, and wrong-method area paths neither authenticate nor
  invoke the topic loader
- Encoded separators are rejected as non-canonical paths before storage even
  though Chi deliberately matches routes against `URL.RawPath`
- Constructor failures cover missing area/topic stores, invalid page bounds,
  missing authentication service, and invalid cookie configuration

Risks / non-goals:

- Area-index cards are not links yet; direct area URLs are now usable
- Topic detail pages, topic creation, replies, moderation, and deployment remain
  separate alpha units

### 2026-09-01 21:24 CDT — Render visible area topic pages

Commit: current commit; hash assigned by Git after commit

Affected files:

- `internal/httpui/area_topic_handler.go`
- `internal/httpui/area_topic_handler_test.go`
- `internal/httpui/view.go`
- `internal/httpui/shell.templ`
- `internal/httpui/shell_templ.go`
- `docs/CHANGELOG.md`

Explanation:

Added the independently testable area topic-list handler and typed complete/HTMX
presentation. It parses only the admitted page query, passes the exact
server-owned access context to one loader, maps persistence rows into narrow
display fields, and builds canonical, topic, previous, and next links through
the validated `/bb` URL authority. Missing/invisible input shares one generic
404; store or malformed-result failures discard partial content and return one
redacted 503.

Verification:

- Complete and HTMX page 2 render escaped area/topic/author text, pinned and
  locked/archived state, reply counts, UTC activity, canonical URL, stable topic
  ID URLs, and page 1/page 3 navigation
- Empty page 1, open/hidden state, singular reply, later previous-page links,
  malformed query, missing slug/area, store failure, malformed loaded rows, and
  committed-write failures are covered
- The handler reaches 97.2% statement coverage under the race detector. The
  only uncovered return is a defensive failure from building
  `/topics/<positive-decimal-id>` after construction has already validated the
  immutable builder; no reachable input can make either segment ambiguous

Risks / non-goals:

- This unit does not wire `/areas/{slug}` into the public/authenticated router,
  add links to area-index cards, or implement the topic page route.

### 2026-09-01 21:08 CDT — Build canonical absolute query URLs

Commit: current commit; hash assigned by Git after commit

Affected files:

- `internal/httpui/url_builder.go`
- `internal/httpui/url_builder_test.go`
- `docs/CHANGELOG.md`

Explanation:

Extended the validated URL builder with application-owned absolute URLs that
include deterministic query values. Path segments retain segment-safe escaping,
query keys retain stable encoding, and neither can replace the configured
public origin or `/bb` prefix. This closes the canonical-link primitive needed
by numbered topic pages.

Verification:

- Empty and numbered page queries, sorted/escaped multi-key queries, and the
  configured prefix produce exact expected absolute URLs
- Ambiguous segments, a zero-value builder, and a deliberately corrupted
  builder fail closed
- The production method has 100% statement coverage under the race detector

Risks / non-goals:

- This method builds only trusted application-owned query values. It is not an
  untrusted return-URL validator and does not replace that separate boundary.

### 2026-09-01 21:00 CDT — Parse canonical topic-page queries

Commit: current commit; hash assigned by Git after commit

Affected files:

- `internal/httpui/topic_page_query.go`
- `internal/httpui/topic_page_query_test.go`
- `docs/CHANGELOG.md`

Explanation:

Added the raw-query parser required by the area topic-list route. An absent
query selects page 1. Otherwise only the literal `page=<decimal>` spelling is
accepted within the caller-supplied positive maximum. Parsing RawQuery directly
prevents duplicate keys, extra parameters, percent encoding, separators, or
`url.Values` error suppression from acquiring accidental meaning.

Verification:

- Canonical page 1, an ordinary page, and the exact maximum are accepted
- Empty values, zero, leading zeros, signs, overflow/excess, case changes,
  percent encoding, plus signs, duplicate and extra keys, semicolons, and an
  invalid parser maximum are rejected
- Input work is capped at the maximum 32-bit decimal spelling, uses constant
  auxiliary space, and the production function has 100% statement coverage
  under the race detector

Risks / non-goals:

- The parser does not choose HTTP status or invoke the store. Those behaviors
  belong to the route handler.

### 2026-09-01 20:53 CDT — Bound visible area topic pages

Commit: current commit; hash assigned by Git after commit

Affected files:

- `internal/store/topic_pages.go`
- `internal/store/topic_pages_test.go`
- `docs/implementation-spec.md`
- `docs/CHANGELOG.md`

Explanation:

Added the store boundary that resolves visible area metadata, rechecks the same
canonical authority in the topic-summary query, and returns one conventional
topic page. The contract fixes page size at 25 and accepted pages at 1 through
10,000. Invalid and empty later pages share the no-row result used by missing
or inaccessible areas. Exact filtered totals and returned row counts are
validated before presentation can consume them.

Verification:

- Visitor, member, moderator, and administrator facts reach both queries
  exactly; page 2 binds offset 25 and limit 25
- Empty page 1, the accepted maximum page, zero/negative/excessive pages,
  canceled context, area/topic failures, and empty later pages are covered
- Oversized, impossible-total, inconsistent-total, and incomplete query results
  fail closed; the production function has 100% statement coverage under the
  race detector

Risks / non-goals:

- Area metadata and topic rows use two individually access-filtered statements,
  not a repeatable-read snapshot. A concurrent access-rule change cannot expose
  topic rows because the second query rechecks authority, but a page may fail
  closed or briefly combine metadata observed immediately before that change.
- HTTP query parsing and rendering remain separate units.

### 2026-09-01 20:43 CDT — Query access-filtered topic summaries

Commit: current commit; hash assigned by Git after commit

Affected files:

- `internal/store/queries/topics.sql`
- `internal/store/db/topics.sql.go`
- `internal/store/db/topics_test.go`
- `internal/store/db/topics_integration_test.go`
- `docs/CHANGELOG.md`

Explanation:

Added the generated PostgreSQL topic-list query for an area slug. The query
repeats the area visibility predicate before returning rows or calculating its
windowed total, excludes soft-deleted topics for every actor, exposes hidden
topics only to staff, and orders pinned topics before unpinned activity with ID
as the deterministic final key. Explicit offset and limit parameters provide
the bounded repository mechanism for conventional pages.

Verification:

- The generated binding preserves context, binds exact access and pagination
  facts, scans every summary field, closes rows, preserves all driver failures,
  and has 100% statement coverage
- PostgreSQL 17 verifies public, authenticated, matching/nonmatching group,
  forged nonmember group, and staff access; visible-empty and missing areas
  produce the same empty result
- PostgreSQL 17 also verifies hidden/deleted exclusion, staff hidden access,
  pinned/activity/ID ordering, locked and archived state, reply count, author,
  exact access-filtered totals, and two-page offset/limit behavior for ten
  race-detector repetitions

Risks / non-goals:

- The generated query trusts an application boundary to bound offsets and
  limits. That store boundary, the HTTP page parser, and the area page itself
  remain separate units.

### 2026-09-01 20:38 CDT — Derive direct-area lookup authority

Commit: current commit; hash assigned by Git after commit

Affected files:

- `internal/store/areas.go`
- `internal/store/areas_test.go`
- `docs/CHANGELOG.md`

Explanation:

Added the store boundary for direct visible-area lookup. It accepts only the
canonical server-owned access snapshot, validates it, derives the closed staff,
member, and local-group facts, and delegates the exact slug plus those facts to
the generated PostgreSQL query. Slugs outside the database schema's exact ASCII
grammar return the same wrapped `pgx.ErrNoRows` as missing and unauthorized
slugs without reaching PostgreSQL.

Verification:

- Visitor, member, moderator, and administrator authority maps to the exact
  generated query parameters; invalid authority and canceled or missing
  dependencies fail before any query
- The 80-byte schema maximum is accepted; empty, 81-byte, uppercase,
  slash-containing, malformed-hyphen, and embedded NUL slugs all return the
  same no-row result without a database call
- Query failures discard any partial fake row, and the new store function has
  100% statement coverage under the race detector

Risks / non-goals:

- This unit does not add the HTTP area route or a topic query. It closes the
  authority and input boundary those later units will call.

### 2026-09-01 20:22 CDT — Enforce visibility on direct area lookup

Commit: current commit; hash assigned by Git after commit

Affected files:

- `internal/store/queries/areas.sql`
- `internal/store/db/areas.sql.go`
- `internal/store/db/area_by_slug_test.go`
- `internal/store/db/areas_integration_test.go`
- `docs/CHANGELOG.md`

Explanation:

Added the generated PostgreSQL lookup required by the future area topic-list
route. The query selects an area by slug only when the caller's server-owned
staff, membership, and local-group facts satisfy the same visibility predicate
as the area index. Missing and unauthorized slugs therefore share the database
no-row result instead of relying on a later application-layer check.

Verification:

- The generated binding passes the exact slug and access facts, scans every
  area field, preserves context, and has 100% statement coverage
- PostgreSQL 17 exercised visitor, forged nonmember group authority,
  empty-group member, matching-group member, nonmatching-group member, and
  staff authority against every seeded area and a missing slug for ten
  race-detector repetitions
- Every unauthorized and missing direct lookup returned the same
  `pgx.ErrNoRows` result

Risks / non-goals:

- This unit does not yet expose `/areas/{slug}` or load topics. It establishes
  the access-controlled repository primitive that route will require.

### 2026-09-01 20:10 CDT — Wire the visible-area index

Commit: current commit; hash assigned by Git after commit

Affected files:

- `internal/httpui/handler.go`
- `internal/httpui/handler_test.go`
- `internal/httpui/authenticated_handler.go`
- `internal/httpui/authenticated_handler_test.go`
- `cmd/forum/main.go`
- `cmd/forum/main_test.go`
- `docs/CHANGELOG.md`

Explanation:

Replaced the process root's static empty-area shell with the admitted
visible-area handler. The root still crosses the session boundary first, then
passes its exact canonical authority through `store.ListVisibleAreas` and the
generated PostgreSQL query. One generated query wrapper is constructed at
startup and reused. Health, static, login, callback, unknown, logout, and
revalidation routing retain their existing boundaries.

Verification:

- The authenticated router proves infrastructure and unknown paths perform no
  session or area lookup, while `/` authenticates once, passes the resulting
  member authority once, and renders the returned area
- The process lifecycle test serves a real anonymous root request, proves the
  exact visitor query facts `(is_staff=false, is_member=false, groups=[])`, and
  renders the database row through the running HTTP server
- Missing area-list authority fails handler construction; HTTP and process
  packages pass the race detector

Risks / non-goals:

- Area cards are not links until the area/topic read route exists. Readiness,
  administration, and deployment remain separate units.

### 2026-09-01 19:54 CDT — Render visible discussion areas

Commit: current commit; hash assigned by Git after commit

Affected files:

- `internal/httpui/area_index_handler.go`
- `internal/httpui/area_index_handler_test.go`
- `internal/httpui/shell.templ`
- `internal/httpui/shell_templ.go`
- `internal/httpui/static/app-1.0.0-alpha.1.css`
- `internal/httpui/static_test.go`
- `docs/CHANGELOG.md`

Explanation:

Added the independently testable discussion-area index handler and real area
list presentation. It passes the exact server-owned session authority to one
injected visible-area lister, renders only the returned names and descriptions
for complete-page and HTMX requests, preserves the established empty state,
and discards partial rows on failure before returning a redacted 503 page.

Verification:

- Grouped authenticated authority reaches the lister unchanged; multiple
  returned areas render with HTML escaping in complete and fragment responses
- Empty, partial-result error, and committed-write failure paths are covered;
  store details and partial area content never reach the failure response
- `newAreaIndexHandler` reaches 100% statement coverage and the HTTP package
  passes the race detector

Risks / non-goals:

- This unit is deliberately not wired into the process router yet. Area links,
  topic summaries, and startup/store composition are separate reviewable units.

### 2026-09-01 19:46 CDT — Canonicalize session access authority

Commit: current commit; hash assigned by Git after commit

Affected files:

- `internal/auth/session_authentication.go`
- `docs/CHANGELOG.md`

Explanation:

Removed authentication's duplicate definitions of `Role` and `AccessContext`.
The authentication package now aliases the canonical policy types and named
role constants, so a session snapshot can reach authorization and repository
boundaries without field-by-field conversion or a second invariant that can
drift. Existing `auth` package names remain valid for callers.

Verification:

- Authentication, HTTP, store, and policy packages pass the race detector with
  the shared types
- Existing session, visibility, publishing, and repository matrices compile
  against and exercise the same canonical authority definition

Risks / non-goals:

- This is a behavior-preserving type consolidation. It does not add an HTTP
  area read or change any permission.

### 2026-09-01 19:32 CDT — Project session groups into access authority

Commit: current commit; hash assigned by Git after commit

Affected files:

- `internal/auth/session_authentication.go`
- `internal/auth/session_authentication_test.go`
- `internal/auth/initial_session_integration_test.go`
- `docs/CHANGELOG.md`

Explanation:

Completed the active-session authority projection by validating the database
membership array as a non-null, strictly ascending set of positive local IDs
and copying it into the request-scoped `AccessContext`. Malformed loader state
fails closed before the conditional session-activity write. The copy prevents a
database adapter or reused scan buffer from mutating authority after the
authentication result has been returned.

Verification:

- Unit tests prove grouped and empty membership snapshots, ownership of the
  returned slice, and rejection of nil, zero, negative, duplicate, and
  descending membership arrays
- PostgreSQL 17 passed ten complete initial-session integrations and projected
  the exact current local membership set into the authenticated access snapshot
- The authentication package passes the race detector

Risks / non-goals:

- Validation and the request-owned copy cost O(g) time and space for g current
  memberships. Group administration and its same-transaction immutable audits
  remain later A1-05 units.

### 2026-09-01 19:24 CDT — Load current session group authority

Commit: current commit; hash assigned by Git after commit

Affected files:

- `internal/store/queries/auth.sql`
- `internal/store/db/auth.sql.go`
- `internal/store/db/auth_active_session_test.go`
- `internal/auth/initial_session_integration_test.go`
- `docs/CHANGELOG.md`

Explanation:

Extended the active-session query with the authenticated user's current local
group IDs. A correlated indexed subquery keeps the session row singular,
returns an empty non-null array for users without memberships, and orders IDs
by the membership primary-key component so authorization snapshots are
deterministic. Forum role, mute state, and memberships now come from one
PostgreSQL statement and one MVCC snapshot; no browser value can supply them.

Verification:

- The generated binding test proves exact token/time arguments, every scanned
  authority field, empty-safe array typing, and the required membership filter
  and order clauses
- PostgreSQL 17 passed ten complete initial-session integrations after two
  memberships were inserted in reverse ID order; every lookup returned the
  exact ascending IDs
- The full repository generation, formatting, vet, race-detector, and coverage
  gate passes

Risks / non-goals:

- The query allocates one `bigint[]` proportional to the user's current local
  membership count, using the existing `(user_id, group_id)` index. Projecting
  and structurally validating that array into `AccessContext` is the next
  separate unit.

### 2026-09-01 19:24 CDT — Enforce topic-creation area policy

Commit: current commit; hash assigned by Git after commit

Affected files:

- `internal/policy/create_topic.go`
- `internal/policy/create_topic_test.go`
- `docs/implementation-spec.md`
- `docs/CHANGELOG.md`

Explanation:

Added the narrow `CanCreateTopic` publishing predicate. It requires canonical
authenticated visibility and denies visitors, suspended actors, and snapshots
with an active mute. Members publish only in normal areas they can view;
moderators and administrators may also publish in read-only areas. Archived
areas deny every actor until an administrator restores the area, preventing an
accidental moderator archival bypass.

Verification:

- The matrix crosses visitor/member/matching and nonmatching group/moderator/
  administrator with public/authenticated/group visibility and normal/read-only/
  archived posting modes
- Suspended and actively muted member/staff snapshots deny, as do malformed
  actors and unknown/impossible area policy values
- The production predicate and entire policy package reach 100% statement
  coverage and pass 50 repeated race-detector runs

Risks / non-goals:

- Snapshot construction must normalize expired mute timestamps to nil at its
  validated time. Rate limiting and the transactional topic insert remain
  separate later units.

### 2026-09-01 19:18 CDT — Derive visible-area query authority

Commit: current commit; hash assigned by Git after commit

Affected files:

- `internal/store/areas.go`
- `internal/store/areas_test.go`
- `docs/implementation-spec.md`
- `docs/CHANGELOG.md`

Explanation:

Added the exported store boundary for visible-area listing. It rejects invalid
access snapshots before database work, derives accepted-member state from
canonical authentication, derives staff bypass only from the closed local
moderator/administrator roles, and passes current verified local group IDs to
the generated PostgreSQL predicate. Request fields cannot supply any of these
facts. Query failure returns no partial rows.

Verification:

- Visitor, member, grouped moderator, and administrator cases bind the exact
  expected internal SQL facts and preserve returned rows
- Contradictory identity, missing/unknown roles, invalid groups, nil
  dependencies, cancellation, and query failure all fail before returning
  content; a query-supplied partial slice is discarded on error
- The production boundary reaches 100% statement coverage and passes 50
  repeated race-detector runs

Risks / non-goals:

- HTTP session construction still needs to populate role and local group facts
  before pages consume this repository. This boundary deliberately accepts no
  browser-supplied authorization fields.

### 2026-09-01 19:12 CDT — Centralize area-view actor validation

Commit: current commit; hash assigned by Git after commit

Affected files:

- `internal/policy/area_view.go`
- `docs/CHANGELOG.md`

Explanation:

Changed `CanViewArea` to use `AccessContext.Valid` instead of carrying a second
copy of anonymous/user/role/group structural checks. Visibility, staff bypass,
group intersection, suspension, and mute behavior are unchanged. There is now
one authority invariant for the next repository translation unit to consume.

Verification:

- The complete area-view matrix and malformed-authority suite remain green
- Both production policy functions retain 100% statement coverage and pass 50
  repeated race-detector runs

Risks / non-goals:

- This is a behavior-preserving mechanism consolidation; it does not add a new
  permission or broaden any actor state.

### 2026-09-01 19:08 CDT — Validate access-context authority

Commit: current commit; hash assigned by Git after commit

Affected files:

- `internal/policy/access_context.go`
- `internal/policy/access_context_test.go`
- `docs/implementation-spec.md`
- `docs/CHANGELOG.md`

Explanation:

Added `AccessContext.Valid` as the structural authority gate before access facts
reach repositories. Canonical anonymous state carries no synthetic user, role,
or groups. Authenticated state requires a positive local user, one closed local
role, and positive local group IDs. Suspension, mute, and validation time remain
facts for operation-specific policy instead of making an otherwise coherent
identity snapshot structurally malformed.

Verification:

- Canonical visitor, member, grouped/suspended member, moderator, and
  administrator snapshots admit
- Contradictory anonymous state, missing/nonpositive users, missing/unknown
  roles, and zero/negative group IDs deny
- The production method reaches 100% statement coverage and passes 50 repeated
  race-detector runs; the entire policy package remains at 100%

Risks / non-goals:

- Repository translation is the next unit. Request fields still must never
  construct or override an `AccessContext` directly.

### 2026-09-01 19:03 CDT — Enforce area-list visibility in PostgreSQL

Commit: current commit; hash assigned by Git after commit

Affected files:

- `internal/store/queries/areas.sql`
- `internal/store/db/areas.sql.go`
- `internal/store/db/areas_test.go`
- `internal/store/db/areas_integration_test.go`
- `docs/implementation-spec.md`
- `docs/CHANGELOG.md`

Explanation:

Added generated `ListVisibleAreas`. PostgreSQL removes unauthorized rows before
materialization and ordering: staff bypass explicitly, visitors receive only
public areas, accepted members add authenticated areas, and group-restricted
areas require a current local group-ID intersection. Nil and empty group arrays
are explicit no-group authority. Results remain stable by display order and ID;
no Go-side filtering can leak restricted counts or row existence.

Verification:

- Generated binding tests prove exact staff/member/group argument order, every
  area field scan, row closure, and preservation of query/scan/iteration errors
- The generated method reaches 100% statement coverage and passes 20 repeated
  race-detector runs
- Five PostgreSQL 17 integration repetitions cover visitor, empty-group member,
  each matching/nonmatching group member, and staff against public,
  authenticated, matching-group, other-group, and unmapped staff-only areas

Risks / non-goals:

- The generated query accepts internal access facts. The next repository unit
  must derive them from `policy.AccessContext`; request data must never bind the
  staff boolean or group list directly.

### 2026-09-01 18:55 CDT — Define the closed area-view policy

Commit: current commit; hash assigned by Git after commit

Affected files:

- `internal/policy/area_view.go`
- `internal/policy/area_view_test.go`
- `docs/implementation-spec.md`
- `docs/CHANGELOG.md`

Explanation:

Added the canonical role, access-context, visibility, posting-mode, and area-
policy types plus the narrow `CanViewArea` predicate. It distinguishes
visibility from publishing state, grants the documented moderator/
administrator read bypass, uses exact local group-ID intersection for members,
and fails closed on contradictory authority, unknown closed values, invalid
group IDs, or impossible group mappings. Suspension and mute state remain
publishing restrictions rather than hidden visibility values.

Verification:

- The full visitor/member/matching-group/nonmatching-group/moderator/
  administrator matrix covers all three visibility values
- Malformed anonymous and authenticated authority, unknown visibility/posting
  values, nonpositive group IDs, and group mappings on non-group areas all deny
- The production predicate reaches 100% statement coverage and passes 50
  repeated race-detector runs

Risks / non-goals:

- This predicate supports mutation decisions and policy explanation. Every
  repository returning area-owned rows must still enforce equivalent access in
  SQL before aggregation or materialization.

### 2026-09-01 18:40 CDT — Add the first-administrator operator command

Commit: current commit; hash assigned by Git after commit

Affected files:

- `cmd/operator/main.go`
- `cmd/operator/main_test.go`
- `cmd/operator/main_integration_test.go`
- `README.md`
- `docs/release-operations.md`
- `docs/CHANGELOG.md`

Explanation:

Added the separate `gotth-bb-operator bootstrap-administrator` executable. It
accepts exactly one issuer, subject, and operator audit identifier; reads only
the redacted database configuration; creates an RFC 4122 version 4 request ID;
opens one direct PostgreSQL connection; and invokes the governed bootstrap once
without retry. It prints only committed user/audit IDs. Connection failures are
redacted, while output failures after a successful transaction explicitly say
that the grant committed so an operator does not mistake reporting failure for
transaction rollback.

Verification:

- The command runner reaches 100% statement coverage; 20 race-detector runs
  cover success, malformed and duplicate arguments, cancellation at each
  boundary, redacted configuration/connection failures, entropy failure,
  invalid results, exact close ownership, and committed-output failure
- Ten PostgreSQL 17 integration repetitions prove the command provisions the
  selected existing identity, writes one operator audit, reports committed IDs,
  and rejects a second attempt without another audit
- The pgx v5.10.0 source contract was checked after integration caught that
  `ConnConfig.ConnString()` returns the originally parsed string rather than a
  serialization of mutated fields; the fixture now constructs its target URL
  explicitly

Risks / non-goals:

- No command was run against a real deployment or non-test identity. Normal
  administrator role and suspension management remain later alpha work.

### 2026-09-01 17:58 CDT — Enforce first-administrator governance transaction

Commit: current commit; hash assigned by Git after commit

Affected files:

- `internal/governance/bootstrap.go`
- `internal/governance/bootstrap_test.go`
- `internal/governance/bootstrap_integration_test.go`
- `docs/implementation-spec.md`
- `docs/CHANGELOG.md`

Explanation:

Added `governance.BootstrapAdministrator`. It validates bounded operator and
identity inputs, locks the seeded governance singleton, requires exactly zero
active administrators, resolves one pre-provisioned external identity, and
executes the coupled role/audit statement before one commit. Missing, suspended,
already-closed, malformed, failed, and unknown-commit cases return no admitted
result and are never retried.

Verification:

- Every invalid input and transaction-stage failure fails before later work and
  returns no user/audit IDs
- PostgreSQL 17 proves missing-identity rollback and concurrent bootstrap
  serialization: exactly one winner, one administrator, and one operator audit
- The audit records the winning operator, target, fixed action, previous member
  role, and resulting administrator role
- The production function reaches 100% statement coverage; 100 repeated unit
  race runs and ten repeated PostgreSQL race runs pass

Risks / non-goals:

- The executable operator command remains the next unit; normal later role and
  suspension transitions must use the same governance lock.

### 2026-09-01 17:49 CDT — Couple administrator bootstrap role and audit writes

Commit: current commit; hash assigned by Git after commit

Affected files:

- `internal/store/queries/foundation.sql`
- `internal/store/db/foundation.sql.go`
- `internal/store/db/foundation_governance_test.go`
- `docs/implementation-spec.md`
- `docs/CHANGELOG.md`

Explanation:

Added generated `BootstrapAdministratorAndAudit`. Inside its caller-owned
governance transaction it row-locks the exact active target user, changes only
the local role and update timestamp, and inserts the required immutable
`actor_kind=operator` / `bootstrap_administrator` audit event from explicit
previous and resulting role JSON. The statement returns both user and audit IDs
or no row; role and audit cannot split.

Verification:

- Generated arguments preserve exact user, timestamp, operator identifier, and
  request UUID order
- Query text retains the active-user predicate, user lock, fixed role/action/
  actor values, and previous/resulting role objects
- Scan failure returns the zero result with its internal cause
- The generated method reaches 100% statement coverage and passes 50 race-
  detector repetitions

Risks / non-goals:

- Governance locking and zero-active-administrator enforcement belong to the
  transaction coordinator in the next unit; this statement cannot be called as
  a stand-alone policy decision.

### 2026-09-01 17:42 CDT — Complete both OIDC purposes at one callback

Commit: current commit; hash assigned by Git after commit

Affected files:

- `internal/httpui/callback_handler.go`
- `internal/httpui/callback_handler_test.go`
- `internal/httpui/revalidation_callback_handler_test.go`
- `internal/httpui/authenticated_handler.go`
- `internal/httpui/authenticated_handler_test.go`
- `cmd/forum/main_test.go`
- `docs/implementation-spec.md`
- `docs/CHANGELOG.md`

Explanation:

Generalized `/auth/callback` over initial login and revalidation. Exactly one
matching fixed state-cookie namespace selects the expected service operation;
the consumed PostgreSQL attempt still enforces durable purpose. Revalidation
requires one strict old session cookie, calls `CompleteRevalidation`, expires
only its transient state cookie, installs the rotated session credential, and
redirects only to the revalidated internal path.

Verification:

- Initial-login callback behavior and failure contracts remain unchanged
- Revalidation propagates exact state, code, and old opaque token, then emits
  only the selected state-cookie expiration plus replacement session cookie
- Missing, duplicate, quoted, short, or malformed old session cookies fail
  before completion
- Missing, duplicate, or ambiguous state namespaces fail before either service
  boundary
- The shared callback function reaches 100% statement coverage; focused callback
  and router suites pass 20 times under the race detector

Risks / non-goals:

- The cookie namespace is only an expected-path selector; PostgreSQL purpose
  validation remains mandatory and fail-closed.
- Protected-route freshness enforcement remains subsequent work.

### 2026-09-01 17:30 CDT — Start revalidation from authenticated browser state

Commit: current commit; hash assigned by Git after commit

Affected files:

- `internal/httpui/authenticated_handler.go`
- `internal/httpui/authenticated_handler_test.go`
- `cmd/forum/main_test.go`
- `docs/implementation-spec.md`
- `docs/CHANGELOG.md`

Explanation:

Added `GET /auth/revalidate` to the authenticated browser router. The route
loads the exact session cookie through the existing session boundary, reads the
positive session ID only from private request context, invokes
`BeginRevalidation`, and emits the distinct revalidation state cookie plus the
provider redirect. No query or form field can provide the session binding.

Verification:

- A live authenticated session propagates its exact server-owned ID and a
  validated internal return path
- Anonymous and internally inconsistent authenticated snapshots return a non-
  cacheable 401 before OIDC attempt creation
- Begin failures remain generic, non-cacheable 503 responses with no redirect
  or state cookie
- Infrastructure and unknown routes still bypass session lookup
- Focused router tests pass 20 times under the race detector

Risks / non-goals:

- Protected-route freshness redirects and dual-purpose callback completion
  remain subsequent units.
- The route intentionally permits a signed-in member to refresh before the
  interval is due; it grants no authority and still requires Authentik success.

### 2026-09-01 17:21 CDT — Separate initial and revalidation browser state

Commit: current commit; hash assigned by Git after commit

Affected files:

- `internal/httpui/login_state_cookie.go`
- `internal/httpui/login_start_handler.go`
- `internal/httpui/login_start_handler_test.go`
- `internal/httpui/authenticated_handler.go`
- `docs/implementation-spec.md`
- `docs/CHANGELOG.md`

Explanation:

Generalized the hardened login-start HTTP boundary over exactly two server-
selected state-cookie namespaces. Initial login retains `_oidc_state`;
revalidation uses `_oidc_revalidate_state`. Browser input cannot select a
purpose, and both flows retain the same strict return-path, authorization-URL,
state, cookie, cache, and redirect controls.

Verification:

- Initial-login cookie name and behavior remain byte-for-byte compatible
- Revalidation emits only the distinct fixed cookie name with identical path,
  lifetime, HttpOnly, Secure, and SameSite policy
- Unknown suffixes fail construction before request work
- The changed production function reaches 100% statement coverage and passes
  20 race-detector repetitions

Risks / non-goals:

- The revalidation route and dual-purpose callback remain subsequent units.
- The fixed suffix is internal construction policy, never a query/form value.

### 2026-09-01 17:13 CDT — Expose revalidation completion through auth service

Commit: current commit; hash assigned by Git after commit

Affected files:

- `internal/auth/service.go`
- `internal/auth/service_revalidation_complete_test.go`
- `internal/auth/initial_session_integration_test.go`
- `docs/implementation-spec.md`
- `docs/CHANGELOG.md`

Explanation:

Added `Service.CompleteRevalidation`. It binds service-owned attempt
consumption, OIDC exchange, entropy, session policy, PostgreSQL transaction,
and exact old browser token to the ordered completion coordinator. Only the
replacement token, validated navigation path, and fresh expiry cross the public
service boundary.

Verification:

- Invalid service/callback state fails before database work
- PostgreSQL 17 plus the real signed OIDC harness proves one-shot attempt
  consumption, one token exchange, old-session revocation, replacement
  authentication, exact validation/expiry timestamps, and replay rejection
- The service method reaches 100% statement coverage with unit and integration
  tests combined
- Three repeated end-to-end PostgreSQL runs pass under the race detector

Risks / non-goals:

- HTTP callback routing and cookie replacement remain subsequent units.
- Provider, database, and transaction failures return no browser credential and
  are not retried.

### 2026-09-01 17:06 CDT — Complete revalidation in one ordered workflow

Commit: current commit; hash assigned by Git after commit

Affected files:

- `internal/auth/complete_revalidation.go`
- `internal/auth/complete_revalidation_test.go`
- `docs/implementation-spec.md`
- `docs/CHANGELOG.md`

Explanation:

Added the fail-closed revalidation completion coordinator. It consumes exactly
one purpose-bound attempt, exchanges the authorization code once, and invokes
atomic session rotation with the attempt's server-owned session ID and the old
browser token. It returns the replacement token and validated navigation path
only after all three stages succeed.

Verification:

- Exact `consume -> exchange -> rotate` ordering and arguments are asserted
- Every stage failure stops later work, returns no browser state, and redacts
  non-context causes
- Cancellation from every stage remains process-inspectable
- Invalid dependencies and incomplete successful stage results fail closed
- The production function reaches 100% statement coverage and passes 20 race-
  detector repetitions

Risks / non-goals:

- Service and HTTP wiring remain subsequent units.
- No stage is retried, including a rotation commit with unknown outcome.

### 2026-09-01 16:59 CDT — Rotate revalidated sessions atomically

Commit: current commit; hash assigned by Git after commit

Affected files:

- `internal/auth/session_rotation.go`
- `internal/auth/session_rotation_test.go`
- `internal/auth/initial_session_integration_test.go`
- `docs/implementation-spec.md`
- `docs/CHANGELOG.md`

Explanation:

Added the revalidated-session rotation transaction. It strictly validates and
hashes the exact old opaque token, locks the bound active session/user/identity,
requires immutable issuer/subject continuity, refreshes only approved OIDC
profile fields, creates a fresh replacement session, and revokes exactly the
old ID/hash before one commit. The replacement credential never escapes a
failed or unknown transaction outcome.

Verification:

- Every input and transaction-stage failure returns no replacement credential
  and rolls back
- PostgreSQL 17 proves profile refresh and local-role preservation, exact old-
  token revocation, replacement authentication, fresh absolute lifetime, and
  identity-mismatch rollback
- Focused unit tests pass 20 times under the race detector
- The production function reaches 100% statement coverage with unit and
  PostgreSQL integration tests combined

Risks / non-goals:

- This unit does not consume the OIDC attempt or exchange the authorization
  code; the completion coordinator remains the next boundary.
- Automatic retries remain forbidden, including unknown commit outcomes.

### 2026-09-01 20:15 CDT — Revoke exact old session during rotation

Commit: current commit; hash assigned by Git after commit

Affected files:

- `internal/store/queries/auth.sql`
- `internal/store/db/auth.sql.go`
- `internal/store/db/auth_rotation_test.go`
- `docs/implementation-spec.md`
- `docs/CHANGELOG.md`

Explanation:

Added the generated `RevokeSessionForRotation` query. It updates only the exact
positive session ID plus old-token hash while requiring the row to remain
unrevoked, issued, and unexpired at the transaction timestamp. The later
rotation coordinator will require exactly one affected row before commit.

Verification:

- Generated method forwards exact context, observed time, ID, and hash order
- Query text retains ID/hash/revocation/issue/expiry predicates
- Zero and one affected rows round-trip exactly for coordinator validation
- Execution failures return zero rows with the original internal cause
- Generated method reaches 100% statement coverage

Risks / non-goals:

- This query is transaction-bound by its future caller; alone it intentionally
  performs no retry and invents no idempotent success.

### 2026-09-01 20:00 CDT — Lock exact active session for rotation

Commit: current commit; hash assigned by Git after commit

Affected files:

- `internal/store/queries/foundation.sql`
- `internal/store/db/foundation.sql.go`
- `internal/store/db/foundation_rotation_test.go`
- `docs/implementation-spec.md`
- `docs/CHANGELOG.md`

Explanation:

Added the generated `GetActiveSessionForRotation` query. It selects only the
exact session ID plus old-token hash while enforcing revocation, issue,
nonfuture activity/validation, absolute-expiry, idle-expiry, and current local
suspension state. It returns the
stored user/issuer/subject/expiry and row-locks the session, user, and identity
together before any rotation write.

Verification:

- Generated method forwards the exact context, ID, 32-byte hash, observed time,
  and idle cutoff in fixed order and scans all identity/session fields
- Query text asserts exact revocation and suspension predicates plus the three-
  row `FOR UPDATE` boundary
- Scan failure returns the zero row and exact cause
- Generated method reaches 100% statement coverage

Risks / non-goals:

- This unit adds no schema migration; it is a query over the admitted alpha
  schema.
- The replacement insert and exact old-session revoke are subsequent units.

### 2026-09-01 19:45 CDT — Consume initial and revalidation attempts distinctly

Commit: current commit; hash assigned by Git after commit

Affected files:

- `internal/auth/login_consume.go`
- `internal/auth/login_consume_test.go`
- `internal/auth/service.go`
- `docs/implementation-spec.md`
- `docs/CHANGELOG.md`

Explanation:

Generalized the one-time callback consumer around an explicit expected purpose.
Initial login still requires no session metadata. Revalidation now requires and
returns one positive stored session ID. Purpose or session mismatches occur only
after the atomic consume and return no recovered nonce, verifier, or path.

Verification:

- Initial attempts preserve the exact zero session binding
- Revalidation attempts recover the exact positive server-stored session ID
- Unknown expected purposes fail before database work
- Wrong purpose and missing, zero, negative, or unexpected session metadata
  burn the row and return no usable material
- `consumeLoginAttempt` reaches 100% statement coverage

Risks / non-goals:

- The initial-login service remains behaviorally unchanged and explicitly asks
  for `purpose=login`.
- Revalidation exchange and rotation consume the new session binding in later
  units.

### 2026-09-01 19:30 CDT — Expose revalidation start through authentication service

Commit: current commit; hash assigned by Git after commit

Affected files:

- `internal/auth/service.go`
- `internal/auth/service_revalidation_begin_test.go`
- `docs/implementation-spec.md`
- `docs/CHANGELOG.md`

Explanation:

Added `Service.BeginRevalidation`. It validates the retained provider/store/
entropy/clock/path dependencies, persists one session-bound revalidation
attempt, constructs the state/nonce/PKCE authorization URL, and returns only
the public URL and state. Non-context causes collapse at the service boundary.

Verification:

- Exact positive session ID, `revalidate` purpose, and return path reach the
  single SQL insert before the provider URL is returned
- Invalid service state and session IDs return no browser material
- Canceled context is preserved; database causes are redacted
- `Service.BeginRevalidation` reaches 91.7% statement coverage

Risks / non-goals:

- The sole uncovered branch handles an authorization-builder error made
  unreachable after the method's provider validation and freshly generated
  fixed-valid material; no fake injection seam is justified.
- The HTTP start boundary remains the next unit.

### 2026-09-01 19:15 CDT — Bind revalidation attempts to server sessions

Commit: current commit; hash assigned by Git after commit

Affected files:

- `internal/auth/revalidation_begin.go`
- `internal/auth/revalidation_begin_test.go`
- `docs/implementation-spec.md`
- `docs/CHANGELOG.md`

Explanation:

Added the protected revalidation-attempt constructor. It accepts only a
positive server-owned session ID, delegates the existing fixed-size state,
nonce, PKCE, protection, return-path, clock, and persistence mechanism, then
rewrites the durable purpose/session metadata before the single insert.

Verification:

- Exact state material and five-minute lifetime match initial-login behavior
- Persisted purpose is `revalidate` with the exact positive session foreign key
- Missing inserter and nonpositive session IDs fail before path or entropy work
- `beginRevalidation` reaches 100% statement coverage

Risks / non-goals:

- The public service and HTTP start boundary are subsequent isolated units.
- Callback consumption and atomic session rotation remain separate work.

### 2026-09-01 19:00 CDT — Retain authenticated session identity

Commit: current commit; hash assigned by Git after commit

Affected files:

- `internal/auth/session_authentication.go`
- `internal/auth/session_authentication_test.go`
- `internal/httpui/session_authentication_handler_test.go`
- `docs/implementation-spec.md`
- `docs/CHANGELOG.md`

Explanation:

Authenticated snapshots now retain the positive local session ID returned by
the existing indexed session lookup. The ID remains server-internal and travels
with the immutable request context; anonymous results preserve the exact zero
value. This supplies the correlation authority needed to bind a revalidation
attempt without accepting a browser-supplied database identifier.

Verification:

- Current and stale authenticated results expose the exact loaded session ID
- HTTP session context preserves the same ID with identity and CSRF state
- Missing, invalid, inactive, and failed lookups still return no session ID
- `authenticateSession` remains at 100% statement coverage

Risks / non-goals:

- No route consumes the ID until the subsequent revalidation-start unit.
- The ID is not rendered, logged, placed in a cookie, or accepted as input.

### 2026-09-01 18:40 CDT — Wire authentication into service startup

Commit: current commit; hash assigned by Git after commit

Affected files:

- `cmd/forum/main.go`
- `cmd/forum/main_test.go`
- `docs/implementation-spec.md`
- `docs/CHANGELOG.md`

Explanation:

The executable now opens its validated PostgreSQL pool, constructs the real
OIDC/session service, activates the authenticated browser router, and only then
binds the HTTP listener. Authentication construction failures are redacted,
preserve cancellation, and close the pool exactly once. Cookie transport is
derived from the immutable public URL scheme.

Verification:

- Startup passes the owned session-capable pool and `/bb/` URL authority to the
  authentication boundary exactly once
- A live internal `GET /login` reaches the activated route and emits the
  expected provider redirect plus `/bb/` state-cookie scope
- Authentication failure, empty service, and cancellation never bind and close
  the pool once without leaking the cause
- Existing pool, listener, cancellation, HTTP lifecycle, and redaction tests
  remain race-clean

Risks / non-goals:

- `run` reaches 92.2% statement coverage, including listener-close and HTTP
  serve failures. Its remaining lines are defensive constructor failures made
  unreachable by earlier validated configuration and fixed nonnil dependencies;
  fake production seams are not added to manufacture those failures. The
  process entrypoint remains separately uninstrumented because it owns signals
  and `os.Exit`.
- Readiness remains deliberately unavailable until its database/migration
  check replaces the current fail-closed response.

### 2026-09-01 18:20 CDT — Construct authentication without exposing secrets

Commit: current commit; hash assigned by Git after commit

Affected files:

- `internal/config/authentication_service.go`
- `internal/config/authentication_service_test.go`
- `docs/implementation-spec.md`
- `docs/CHANGELOG.md`

Explanation:

Added the narrow configuration-owned authentication-service constructor. It
revalidates the immutable environment, public URL, issuer, client identity,
and production secret requirement; computes the exact `/auth/callback`; and
passes the retained client secret only into the concrete authentication
service. No general secret getter was added.

Verification:

- Controlled confidential-client discovery succeeds exactly once and performs
  no PostgreSQL operation during construction
- Invalid configuration and runtime dependencies fail before discovery
- Service and configuration formatting expose neither retained secret
- `Config.NewAuthenticationService` reaches 100% statement coverage

Risks / non-goals:

- Construction performs bounded OIDC discovery and can therefore fail startup
  when Authentik metadata is unavailable or invalid, as specified.
- The executable adopts this constructor in the next isolated wiring unit.

### 2026-09-01 18:00 CDT — Activate browser authentication routes

Commit: current commit; hash assigned by Git after commit

Affected files:

- `internal/httpui/authenticated_handler.go`
- `internal/httpui/authenticated_handler_test.go`
- `docs/implementation-spec.md`
- `docs/CHANGELOG.md`

Explanation:

Added one browser-router constructor that activates initial login, the exact
OIDC callback, authenticated root session loading, and local CSRF-protected
logout through the authentication service. Infrastructure and unknown paths
bypass session lookup so database failure cannot break liveness, static assets,
or deterministic not-found responses.

Verification:

- Login, callback, authenticated root, and logout reach exactly their intended
  service methods and retain bounded route attribution
- Health, static, and unknown routes never invoke session authentication even
  when a session cookie is present
- Logout uses the concrete 4 KiB bounded CSRF validator and revokes once
- Constructor dependency failures return no partial handler

Risks / non-goals:

- `NewAuthenticatedHandler` reaches 87.9% statement coverage. The uncovered
  lines are defensive error returns from later constructors after the same
  builder and cookie inputs have already passed stricter earlier constructors;
  injecting impossible failures solely for a percentage would add fake
  production seams.
- The executable does not construct this router until the next startup-wiring
  unit. Revalidation and first-administrator operation remain separate work.

### 2026-09-01 17:30 CDT — Preserve authenticated route attribution

Commit: current commit; hash assigned by Git after commit

Affected files:

- `internal/httpui/session_authentication_handler.go`
- `internal/httpui/session_authentication_handler_test.go`
- `docs/CHANGELOG.md`

Explanation:

The session boundary now copies the downstream matched route pattern back to
the caller-owned request after adding authentication context. The copy runs
during panic unwinding as well as ordinary completion, preserving bounded route
attribution for both access and recovery logs.

Verification:

- Ordinary authenticated dispatch preserves the downstream route pattern
- Anonymous downstream panic preserves the route pattern and original panic
- `newSessionAuthenticationHandler` remains at 100% statement coverage

Risks / non-goals:

- This changes no authentication, cookie, CSRF, routing, or response behavior.

### 2026-09-01 17:20 CDT — Validate CSRF submissions in constant time

Commit: current commit; hash assigned by Git after commit

Affected files:

- `internal/httpui/csrf_validation.go`
- `internal/httpui/csrf_validation_test.go`
- `docs/implementation-spec.md`
- `docs/CHANGELOG.md`

Explanation:

Added the concrete mutation CSRF validator. HTMX supplies one exact header
without body access. Ordinary forms supply one hidden field in a caller-bounded
URL-encoded body that is restored byte-for-byte. Both channels require strict
32-byte base64url values and use fixed-length constant-time comparison against
the authenticated request token.

Verification:

- Exact header and form values pass; header validation never reads the body
- Valid form bodies are closed and restored exactly for downstream parsing
- Method, context, bounds, content type, body lifecycle, encoding, cardinality,
  and mismatch failures all fail closed
- Streaming and declared oversize bodies are rejected
- `validateCSRFRequest` reaches 100% statement coverage

Risks / non-goals:

- Multipart ordinary forms will require a route-specific bounded strategy;
  arbitrary multipart parsing is deliberately not smuggled into this validator.

### 2026-09-01 17:00 CDT — Bind CSRF authority to authenticated requests

Commit: current commit; hash assigned by Git after commit

Affected files:

- `internal/httpui/session_authentication_handler.go`
- `internal/httpui/session_authentication_handler_test.go`
- `docs/implementation-spec.md`
- `docs/CHANGELOG.md`

Explanation:

Extended session loading to derive and attach a private CSRF synchronizer token
only after the opaque credential authenticates. Anonymous requests receive no
token. An impossible authenticated result paired with a malformed credential
fails closed, expires the browser cookie, and never reaches the application.

Verification:

- Exact authenticated snapshots receive the independently verified derived
  token
- Anonymous requests receive an empty CSRF context
- Parallel requests keep both identity and CSRF state isolated
- Authenticated malformed credentials fail 500 and expire browser state
- `newSessionAuthenticationHandler` remains at 100% statement coverage

Risks / non-goals:

- The next unit validates submitted header/form tokens in constant time.

### 2026-09-01 16:45 CDT — Derive session-bound CSRF tokens

Commit: current commit; hash assigned by Git after commit

Affected files:

- `internal/httpui/csrf_token.go`
- `internal/httpui/csrf_token_test.go`
- `docs/implementation-spec.md`
- `docs/CHANGELOG.md`

Explanation:

Added deterministic CSRF synchronizer-token derivation. The exact decoded
256-bit opaque session secret keys HMAC-SHA-256 over the fixed
`gotth-bb/csrf/v1` domain; only the 43-character base64url digest is exposed to
forms. It rotates with the session, cannot authenticate, and requires no new
durable secret or database field.

Verification:

- Output matches an independently calculated OpenSSL HMAC vector
- Derived output is distinct from the session credential and strictly decodes
  to 32 bytes
- Missing, short, and invalid-encoding session values fail closed
- `deriveCSRFToken` reaches 100% statement coverage

Risks / non-goals:

- The session middleware writer and constant-time request validator follow.

### 2026-09-01 16:30 CDT — Define request CSRF-token lookup

Commit: current commit; hash assigned by Git after commit

Affected files:

- `internal/httpui/csrf_context.go`
- `internal/httpui/csrf_context_test.go`
- `docs/CHANGELOG.md`

Explanation:

Defined the private request-context slot and typed lookup for the session-bound
CSRF synchronizer token. Nil, absent, and wrong-typed state returns empty so
mutation validation fails closed.

Verification:

- Stored string tokens round-trip exactly
- Nil, absent, and wrong-typed context state is empty
- `csrfTokenFromContext` reaches 100% statement coverage

Risks / non-goals:

- Token derivation and the session-bound writer follow in subsequent units.

### 2026-09-01 16:15 CDT — Enforce local logout ordering

Commit: current commit; hash assigned by Git after commit

Affected files:

- `internal/httpui/logout_handler.go`
- `internal/httpui/logout_handler_test.go`
- `docs/CHANGELOG.md`

Explanation:

Added the exact authenticated POST logout boundary. Construction requires a
CSRF validator. Requests revoke local server state before expiring the cookie
and redirecting to the application root; an idempotent no-row result is still a
successful logout. Stale Authentik validation never prevents local logout.

Verification:

- CSRF runs before revocation and both true/false revocation outcomes clear the
  exact path-scoped cookie then return an empty-body 303
- Wrong method, anonymous state, CSRF failure, and invalid cookie cardinality
  stop before revocation
- Revocation failure is a generic 503 that preserves the browser cookie
- Constructor rejects every missing or invalid dependency
- `newLogoutHandler` reaches 100% statement coverage

Risks / non-goals:

- The handler cannot be wired without the subsequent concrete CSRF validator.

### 2026-09-01 16:00 CDT — Expose idempotent session revocation

Commit: current commit; hash assigned by Git after commit

Affected files:

- `internal/auth/service.go`
- `internal/auth/service_revoke_test.go`
- `internal/auth/initial_session_integration_test.go`
- `docs/CHANGELOG.md`

Explanation:

Added the public service method that binds strict session revocation to the
service-owned sqlc query and clock. Logout callers receive only a boolean
revocation outcome and a redacted error contract; they do not own hashes,
timestamps, or SQL.

Verification:

- Nil, zero, and every incomplete service form fails closed
- Malformed credentials are false/no-error without PostgreSQL work
- PostgreSQL 17.10 proves service-owned true revocation and false repeat
- `Service.RevokeSession` reaches 100% statement coverage

Risks / non-goals:

- The POST logout boundary and browser-cookie expiration remain the next unit.

### 2026-09-01 15:35 CDT — Validate and revoke opaque credentials

Commit: current commit; hash assigned by Git after commit

Affected files:

- `internal/auth/session_revocation.go`
- `internal/auth/session_revocation_test.go`
- `docs/CHANGELOG.md`

Explanation:

Added the credential-to-revocation core. It strictly validates the fixed
base64url token, hashes the exact encoded browser bytes, obtains one
microsecond-precision observation time, and delegates one idempotent update.
Malformed state is a no-op; database failures are redacted and cancellation is
preserved.

Verification:

- Exact hash and UTC/microsecond timestamp projection for zero/one-row outcomes
- Missing, short, and invalid-encoding credentials perform no database work
- Nil dependencies, zero time, and impossible row counts fail closed
- Database causes do not leak and context cancellation remains inspectable
- `revokeSession` reaches 100% statement coverage

Risks / non-goals:

- The public service method and POST logout HTTP boundary remain subsequent
  units.

### 2026-09-01 15:20 CDT — Revoke sessions by opaque credential hash

Commit: current commit; hash assigned by Git after commit

Affected files:

- `internal/store/queries/auth.sql`
- `internal/store/db/auth.sql.go`
- `internal/auth/initial_session_integration_test.go`
- `docs/CHANGELOG.md`

Explanation:

Added the generated one-row session revocation query. It targets only the
SHA-256 hash of the exact opaque browser credential, refuses observations before
session issuance, and is idempotent once `revoked_at` is set.

Verification:

- PostgreSQL 17.10 proves wrong-token and pre-issuance observations update zero
- The exact active token updates one row and a repeat updates zero
- Revoked state immediately disappears from active-session lookup and cannot be
  touched
- sqlc regeneration is reproducible

Risks / non-goals:

- Credential validation/service binding and the POST logout boundary follow in
  subsequent units.

### 2026-09-01 15:00 CDT — Load sessions at the HTTP boundary

Commit: current commit; hash assigned by Git after commit

Affected files:

- `internal/httpui/session_authentication_handler.go`
- `internal/httpui/session_authentication_handler_test.go`
- `docs/CHANGELOG.md`

Explanation:

Added the HTTP session boundary. It admits at most one unquoted configured
cookie, delegates current local authentication once, attaches the typed result
to the request, and varies downstream responses by cookie state. Missing state
is anonymous. Duplicate, quoted, malformed, and inactive state is anonymous and
expired. PostgreSQL/authentication failures stop with a generic non-cacheable
503 rather than silently becoming anonymous.

Verification:

- Authenticated and revalidation-required snapshots reach downstream exactly
- Missing state performs no authentication work
- Inactive, duplicate, and quoted cookies expire at the exact application path
- Authentication errors are redacted 503 responses and do not call downstream
- Constructor dependency failures are fail-closed
- `newSessionAuthenticationHandler` reaches 100% statement coverage

Risks / non-goals:

- Route composition and protected-route revalidation policy remain subsequent
  units; this boundary reports the freshness fact without inventing a redirect.

### 2026-09-01 14:40 CDT — Define request authentication lookup

Commit: current commit; hash assigned by Git after commit

Affected files:

- `internal/httpui/session_context.go`
- `internal/httpui/session_context_test.go`
- `docs/CHANGELOG.md`

Explanation:

Defined the request-scoped authentication context key and the single typed
lookup used by HTTP handlers. Missing, nil, or malformed context state resolves
to the exact anonymous zero value rather than a synthetic identity.

Verification:

- Complete authenticated snapshots round-trip without interpretation
- Nil, absent, and wrong-typed context values are anonymous
- `sessionAuthenticationFromContext` reaches 100% statement coverage

Risks / non-goals:

- The next unit is the only writer of this private request-context key.

### 2026-09-01 14:25 CDT — Expose session authentication through the service

Commit: current commit; hash assigned by Git after commit

Affected files:

- `internal/auth/service.go`
- `internal/auth/service_authenticate_test.go`
- `internal/auth/initial_session_integration_test.go`
- `docs/CHANGELOG.md`

Explanation:

Added the public service method that binds opaque-session authentication to the
service-owned PostgreSQL queries, clock, idle timeout, and revalidation policy.
HTTP middleware can now receive only the typed authentication result instead of
owning SQL or policy durations.

Verification:

- Nil, zero, and incomplete service values return zero/error
- Malformed cookie state returns the anonymous zero result without PostgreSQL
  work
- Real PostgreSQL 17.10 proves the full service-owned credential lookup,
  revalidation decision, typed member access, and throttled activity update
- `Service.AuthenticateSession` reaches 100% statement coverage in local and
  tagged integration suites

Risks / non-goals:

- HTTP cookie loading and request-context propagation remain the next unit.

### 2026-09-01 14:15 CDT — Retain complete session policy in authentication

Commit: current commit; hash assigned by Git after commit

Affected files:

- `internal/auth/service.go`
- `internal/auth/service_test.go`
- `docs/CHANGELOG.md`

Explanation:

Extended the immutable authentication constructor to retain the idle timeout
and Authentik revalidation interval beside absolute session age. Startup rejects
either interval below one second or above the absolute lifetime before OIDC
discovery or PostgreSQL work. The service now owns every duration needed for
session authentication instead of requiring request handlers to reinterpret
configuration.

Verification:

- Exact one-second, one-nanosecond-above, and normal-duration construction
- Sub-second idle/revalidation and values one nanosecond above maximum fail
  before a panic-on-use discovery transport can run
- All retained duration values match the constructor inputs
- `NewService` remains at 100% statement coverage

Risks / non-goals:

- The next unit exposes session authentication through the service. Process
  wiring remains later.

### 2026-09-01 14:05 CDT — Authenticate opaque sessions into local access facts

Commit: current commit; hash assigned by Git after commit

Affected files:

- `internal/auth/session_authentication.go`
- `internal/auth/session_authentication_test.go`
- `internal/store/queries/auth.sql`
- `internal/store/db/auth.sql.go`
- `internal/auth/initial_session_integration_test.go`
- `docs/implementation-spec.md`
- `docs/CHANGELOG.md`

Explanation:

Added the session-authentication core. It strictly decodes the 256-bit opaque
cookie value, hashes the exact encoded bytes, loads one active local snapshot,
maps the closed database role into the canonical typed `AccessContext`, marks
the exact revalidation boundary, and performs the conditional activity write
at a five-minute maximum threshold reduced to half a shorter idle timeout.
Invalid/missing/no-row credentials are anonymous;
database and corrupt-row failures deny authentication with redacted errors.

The active query was narrowed to the authorization facts actually consumed by
this boundary instead of dragging display profile fields through every request.

Verification:

- Exact credential hash, observation/idle thresholds, local member/moderator/
  administrator mapping, mute state, and validation timestamp
- Missing, short, malformed, and absent credentials return the exact anonymous
  zero value without a touch
- Exact five-minute and short-idle half-time touch boundaries plus the exact
  revalidation boundary, including benign concurrent zero-row touch results
- Sub-second idle and revalidation policy is rejected as below supported
  browser/PostgreSQL precision
- Every dependency, context, duration, clock, row-invariant, unknown-role,
  query-failure, touch-failure, impossible row-count, and cancellation path
- Non-context database causes are redacted; cancellation remains detectable
- `authenticateSession` reaches 100% statement coverage
- Real PostgreSQL 17.10 proves full cookie-to-access behavior and the exact
  throttled write

Risks / non-goals:

- Group IDs remain empty until A1-05 adds the access-model membership lookup.
- The service method and HTTP middleware bind this core in subsequent units.

### 2026-09-01 13:50 CDT — Throttle session activity writes

Commit: current commit; hash assigned by Git after commit

Affected files:

- `internal/store/queries/auth.sql`
- `internal/store/db/auth.sql.go`
- `internal/auth/initial_session_integration_test.go`
- `docs/CHANGELOG.md`

Explanation:

Added the conditional `last_seen_at` update used only after the service observes
that its fixed activity-write threshold elapsed. The update rechecks that the
session remains unrevoked, unexpired, older than the threshold, and strictly
behind the observation time. Normal requests therefore perform no write.

Verification:

- Real PostgreSQL 17.10 updates one due active session to the exact observation
  time
- Repeating the same touch updates zero rows
- A revoked session updates zero rows even when its timestamps otherwise qualify
- The active-session read remains separate, leaving the no-touch hot path at one
  indexed database round trip
- Generated sqlc output is reproducible and all repository tests pass

Risks / non-goals:

- The service-level authentication method decides the fixed throttle and treats
  concurrent zero-row touches as benign; that is the next unit.

### 2026-09-01 13:45 CDT — Add the active-session lookup primitive

Commit: current commit; hash assigned by Git after commit

Affected files:

- `internal/store/queries/auth.sql`
- `internal/store/db/auth.sql.go`
- `internal/auth/initial_session_integration_test.go`
- `docs/CHANGELOG.md`

Explanation:

Added the single indexed lookup that joins an opaque-token hash to its current
session and local user facts. It admits only unrevoked sessions strictly before
absolute expiry and strictly inside idle expiry, and rejects a currently local-
suspended user in the same PostgreSQL snapshot. It returns the current local
role/profile and validation timestamps needed by the later request boundary.

Verification:

- Real PostgreSQL 17.10 proves exact token-hash lookup and the returned session,
  member role, profile, issuance, activity, validation, and expiry facts
- Exact idle and absolute expiry boundaries return `pgx.ErrNoRows`
- Indefinite local suspension returns no row; the exact suspension-end boundary
  restores the same session
- Revocation immediately returns no row
- Generated sqlc output is reproducible and all repository tests pass

Risks / non-goals:

- This query deliberately does not update `last_seen_at`; the service layer
  will issue a conditional write only after the configured throttle elapses.
- Revalidation policy and browser middleware consume these facts in subsequent
  units.

### 2026-09-01 13:25 CDT — Require browser-owned callback state

Commit: current commit; hash assigned by Git after commit

Affected files:

- `internal/httpui/callback_handler.go`
- `internal/httpui/callback_handler_test.go`
- `docs/implementation-spec.md`
- `docs/CHANGELOG.md`

Explanation:

Strengthened the initial callback to require exactly one unquoted transient
cookie whose value matches the exact query state. Missing, duplicate, quoted,
mismatched, and malformed state now fail before PostgreSQL or the provider. A
valid match is expired before completion, so success and every later failure
rotate that browser binding away. This makes overwriting the one cookie slot at
login start the enforced per-browser outstanding-attempt limit.

Verification:

- Missing, unrelated, mismatched, duplicate, quoted, and invalid matching
  cookies cannot invoke completion or produce response state; exact 43-byte
  values are compared in constant time
- Successful callback emits the state-cookie deletion before the new session
  cookie and redirects with an empty `303` body
- Completion, unsafe return path, malformed session token, and invalid session
  expiry failures retain only the state-cookie deletion and no redirect
- Exact 8,192-byte callback-query boundary remains enforced before cookie or
  completion work
- Go 1.26 `Request.CookiesNamed` behavior was checked in the standard-library
  source; duplicate parsed names are preserved and excess cookie counts fail
  closed as an empty result
- Race-enabled callback tests pass; the handler reaches 96.3% statement
  coverage, with only the defensive validation failure of a cookie already
  constructed valid and then changed to mechanically valid deletion fields

Risks / non-goals:

- Provider-denial callbacks without a code remain generic failures and leave
  their cookie until it is overwritten or expires after five minutes.
- Process route registration remains a later unit.

### 2026-09-01 13:15 CDT — Enforce the login-start HTTP boundary

Commit: current commit; hash assigned by Git after commit

Affected files:

- `internal/httpui/login_start_handler.go`
- `internal/httpui/login_start_handler_test.go`
- `docs/implementation-spec.md`
- `docs/CHANGELOG.md`

Explanation:

Added the exact `GET /login` boundary. It accepts no query or exactly one
canonical `return` value, validates that path inside the application subtree
before authentication work, invokes login start once, then validates the
service result before committing one transient state cookie and one `303`
provider redirect. All failures are generic and non-cacheable.

Verification:

- Default application-root and explicit canonical return paths
- Wrong method, malformed, duplicate, extra, external, noncanonical, and
  oversized query rejection before the service can run
- Service failure returns `503` without reflecting its URL, state, or cause
- Relative, non-HTTP(S), credential-bearing, fragmented, oversized,
  noncanonical, missing-state, mismatched-state, and invalid-state provider
  results cannot set a cookie or redirect
- Parallel race testing exposed and removed one captured constructor error
  variable; every request now owns its validation state
- `newInitialLoginStartHandler` reaches 100% statement coverage

Risks / non-goals:

- The route is not registered until process-level authentication dependencies
  are wired. The callback's matching-cookie requirement is the next unit.

### 2026-09-01 13:05 CDT — Bind login state to one browser cookie

Commit: current commit; hash assigned by Git after commit

Affected files:

- `internal/httpui/login_state_cookie.go`
- `internal/httpui/login_state_cookie_test.go`
- `docs/CHANGELOG.md`

Explanation:

Added the transient browser cookie used to bind one initial OIDC attempt to the
browser that started it. The cookie name is derived from the configured session
cookie name, its value is the exact 256-bit public state, its path is the
application subtree, and its five-minute `Max-Age` matches the database attempt
lifetime. The subsequent handlers use that single derived cookie slot so a new
login replaces the browser's prior usable state.

Verification:

- Exact derived name, 43-character state, `/bb/` path, five-minute `Max-Age`,
  host-only scope, `HttpOnly`, `SameSite=Lax`, and production `Secure` flag
- Explicit insecure-cookie support for loopback HTTP development
- Invalid configured names, browser magic prefixes, zero URL builder, short,
  empty, and malformed state all return a zero cookie
- `newInitialLoginStateCookie` reaches 95.0% statement coverage; the only
  uncovered branch is final `http.Cookie.Valid` failure after every field's
  stricter constructor precondition has passed

Risks / non-goals:

- The login-start and callback handlers consume this constructor in the next
  units. This commit alone does not register a route or set a browser cookie.

### 2026-09-01 12:55 CDT — Start login through the authentication service

Commit: current commit; hash assigned by Git after commit

Affected files:

- `internal/auth/service.go`
- `internal/auth/service_begin_test.go`
- `internal/auth/initial_session_integration_test.go`
- `docs/CHANGELOG.md`

Explanation:

Added the service-level login-start method. It validates the retained service,
validates the internal return path before entropy or PostgreSQL work, creates
and protects one five-minute attempt, performs one synchronous insert, and
builds the Authentik Authorization Code URL. Only the authorization URL and
public state value cross the package boundary; the nonce and PKCE verifier stay
protected in PostgreSQL.

Verification:

- Nil, zero, incomplete, nil-context, canceled-context, invalid-return-path,
  and database-failure cases return zero browser state
- Return-path and database causes are redacted; cancellation remains detectable
- Real PostgreSQL 17.10 plus the controlled issuer proves the stored live
  attempt and exact state/nonce/S256-PKCE authorization request
- The URL contains neither the PKCE verifier nor the OAuth client secret
- `Service.BeginInitialLogin` reaches 91.7% tagged statement coverage; its only
  uncovered branch is the defensive URL-builder failure after the same
  provider preconditions and generated-material invariants have already passed

Risks / non-goals:

- Browser ownership of the state is the next HTTP unit. Until that boundary is
  wired, this method deliberately does not impose cookie policy.
- The method performs no retry. A failed insert returns no browser material.

### 2026-09-01 12:50 CDT — Bind callback completion to the service

Commit: current commit; hash assigned by Git after commit

Affected files:

- `internal/auth/service.go`
- `internal/auth/service_complete_test.go`
- `internal/auth/initial_session_integration_test.go`
- `docs/CHANGELOG.md`

Explanation:

Added the service-level completion method used by the HTTP callback. It binds
the service-owned attempt consumer, hardened provider exchange, and atomic
identity/session transaction to the previously admitted exactly-once
coordinator. Only the opaque browser token, validated internal return path, and
expiry cross the package boundary; every error returns zero browser state.

Verification:

- Nil, zero, and incomplete service values reject without database work; empty
  callback inputs reject before a query
- Real PostgreSQL 17.10 plus the controlled ES256 issuer proves attempt insert
  and consumption, one confidential PKCE exchange, approved profile admission,
  member-only role creation despite an injected administrator claim, exact
  session creation, and browser result projection
- Replaying the consumed state returns zero values and does not issue a second
  token request
- `Service.CompleteInitialLogin` at 100% statement coverage in the tagged
  integration suite; auth package 96.2% with the real boundary enabled

Risks / non-goals:

- This method is the initial-login path only. Revalidation must additionally
  verify and revoke the old session in its transaction.
- Login-start and process/route wiring remain subsequent units.

### 2026-09-01 12:35 CDT — Own authentication dependencies at startup

Commit: current commit; hash assigned by Git after commit

Affected files:

- `internal/auth/service.go`
- `internal/auth/service_test.go`
- `docs/implementation-spec.md`
- `docs/CHANGELOG.md`

Explanation:

Added the immutable authentication service constructor and its exact PostgreSQL
interface. Startup now validates the database, entropy, clock, session lifetime,
and return-path validator before performing one hardened OIDC discovery. A
failure returns no partial service and performs no PostgreSQL work.

The service redacts its entire retained state for every formatting verb because
it owns the OIDC client secret, PostgreSQL object, and entropy source. Session
maximum age is rejected below one second: Go serializes browser-cookie expiry at
whole-second precision even though PostgreSQL accepts microseconds.

Verification:

- Controlled issuer discovery and exact retained provider/database/session
  dependencies
- Every invalid local dependency and pre-canceled context rejects before a
  panic-on-use transport can perform discovery
- Discovery failure returns no partial service
- Whole-second cookie boundary covered one nanosecond below, exactly at, one
  nanosecond above, and materially beyond
- Five formatting verbs produce only `[REDACTED AUTH SERVICE]`
- `NewService` and `Service.Format` at 100% statement coverage; race-enabled
  auth package tests pass

Risks / non-goals:

- Construction performs OIDC discovery and therefore must complete before the
  listener is bound; PostgreSQL readiness remains a separate startup check.
- Login begin/completion methods and process-level secret wiring are subsequent
  units.

### 2026-09-01 12:25 CDT — Enforce the browser callback boundary

Commit: current commit; hash assigned by Git after commit

Affected files:

- `internal/httpui/callback_handler.go`
- `internal/httpui/callback_handler_test.go`
- `docs/CHANGELOG.md`

Explanation:

Added the exact OIDC success-callback HTTP boundary. It accepts only `GET` with
one nonempty `state` and one nonempty `code` inside an 8 KiB raw-query limit,
calls login completion once, revalidates the returned navigation target against
the immutable `/bb` authority, validates the live session cookie, then sets the
cookie and internal `Location` before committing one `303 See Other` response.

All callback responses disable caching. Malformed callbacks and completion
failures return one generic authentication error without reflecting query
values or lower-layer causes; unsafe successful results fail as server
invariants before any cookie or redirect header is written.

Verification:

- Exact completion arguments, one call, cookie attributes, internal redirect,
  empty success body, and no-store headers
- Wrong method, empty/missing/duplicate/extra/malformed parameters, and the
  8 KiB boundary reject before completion
- Injected provider/database-style cause, state, and code do not appear in the
  failure body or browser state
- External redirect, invalid token, and missing expiry produce no cookie or
  `Location`
- `newInitialLoginCallbackHandler` at 100% statement coverage; race-enabled
  HTTP UI package tests pass

Risks / non-goals:

- This unit handles the successful authorization-code response shape documented
  by Authentik. Provider-denial callbacks do not consume an attempt here; the
  bounded attempt expires after five minutes.
- Route registration and binding the concrete authentication service are the
  next wiring units.

### 2026-09-01 12:16 CDT — Complete initial login exactly once

Commit: current commit; hash assigned by Git after commit

Affected files:

- `internal/auth/complete_login.go`
- `internal/auth/complete_login_test.go`
- `docs/CHANGELOG.md`

Explanation:

Added the initial callback coordinator. It consumes the one-time attempt before
performing exactly one OIDC exchange, then creates the identity and session
exactly once. Browser credential, validated return path, and expiry are returned
only after all three stages succeed with complete results.

Every non-context lower-layer failure is collapsed to its stage name at this
trust boundary. Database causes, provider response details, state, code, claims,
and tokens therefore cannot escape through the returned error; cancellation is
preserved for lifecycle handling.

Verification:

- Exact consume → exchange → create ordering and argument/result transfer
- Failure at each stage stops later work, returns no browser result, and
  redacts an injected secret cause
- Nil dependencies, empty state/code, pre-canceled context, cancellation raised
  by every stage, and incomplete successful-stage results fail closed
- `completeInitialLogin` at 100% statement coverage; race-enabled auth package
  tests pass

Risks / non-goals:

- This coordinator deliberately does not retry any stage. A consumed attempt
  remains consumed after exchange or persistence failure.
- HTTP query parsing, cookie writing, redirect status, and browser error pages
  remain in the next handler unit.

### 2026-09-01 12:05 CDT — Build exact session cookies

Commit: current commit; hash assigned by Git after commit

Affected files:

- `internal/httpui/session_cookie.go`
- `internal/httpui/session_cookie_test.go`
- `docs/CHANGELOG.md`

Explanation:

Added the browser session-cookie constructor. It accepts only the canonical
256-bit opaque token, derives the subtree-safe path from the immutable URL
builder, and emits a host-only `HttpOnly`, `SameSite=Lax` cookie with explicit
expiry. Trusted environment wiring selects `Secure`; production must pass true
while explicit HTTP development passes false.

The complete cookie is checked with Go's documented `http.Cookie.Valid`
contract before it reaches `http.SetCookie`, which otherwise silently drops an
invalid cookie name.

Verification:

- Exact `/bb/` path, credential, expiry, `HttpOnly`, `Secure`, and
  `SameSite=Lax` attributes
- Explicit HTTP-development behavior without an accidental `Secure` attribute
- Empty/invalid name, zero URL builder, wrong-length or invalid token, and
  zero/invalid expiry return no cookie
- `newSessionCookie` at 100% statement coverage; race-enabled HTTP UI package
  tests pass

Risks / non-goals:

- This constructor does not write a response header. The callback handler owns
  the single `http.SetCookie` call after authentication and persistence succeed.
- Logout expiration is a separate constructor because it intentionally uses an
  empty value and past expiry rather than a live 256-bit credential.

### 2026-09-01 11:48 CDT — Commit identity and session atomically

Commit: current commit; hash assigned by Git after commit

Affected files:

- `internal/auth/initial_session.go`
- `internal/auth/initial_session_test.go`
- `internal/auth/initial_session_integration_test.go`
- `docs/CHANGELOG.md`

Explanation:

Added the initial-login transaction boundary. It generates one opaque browser
credential, serializes creation by verified issuer and subject, creates or
refreshes only the approved local profile snapshot, preserves forum-local
authorization, and inserts the hashed session before one commit. Any entropy,
database, session-insert, rollback, or unknown-commit failure returns no browser
credential and is never retried by this boundary.

Session issue and expiry times are normalized to PostgreSQL's microsecond
precision before persistence. A positive duration that would collapse to a
non-advancing expiry is rejected before entropy or database work.

Verification:

- PostgreSQL 17.10 first login creates one member, one external identity, and
  one session whose stored hash and timestamps exactly match the returned
  browser credential and configured lifetime
- A later login refreshes the approved profile while preserving an assigned
  local moderator role
- Concurrent first logins for one issuer/subject converge on one user and
  create two distinct sessions without an orphan account
- A duplicate session hash forces the whole profile/session transaction to
  roll back and returns no credential
- PostgreSQL duration boundary checks cover one nanosecond below, exactly at,
  and one nanosecond above its one-microsecond timestamp precision
- Three fresh race-enabled PostgreSQL 17.10 integration runs; auth package
  95.7% statement coverage and `createInitialSession` 88.1%

Risks / non-goals:

- Concrete database error branches that require corrupting or removing schema
  mid-transaction remain uncovered; no fake sqlc/database framework was added
  to manufacture them.
- This unit does not yet set a cookie or expose the callback route. Browser
  rotation and error collapsing belong to the HTTP callback boundary.

### 2026-09-01 11:34 CDT — Generate opaque session material

Commit: current commit; hash assigned by Git after commit

Affected files:

- `internal/auth/session_material.go`
- `internal/auth/session_material_test.go`
- `docs/implementation-spec.md`
- `docs/CHANGELOG.md`

Explanation:

Added fixed session material generation. It reads 256 bits from the explicit
entropy source, encodes the browser cookie as 43 unpadded base64url characters,
and hashes those exact encoded bytes with SHA-256 for PostgreSQL lookup. Mutable
source and hashing copies are cleared before return.

Verification:

- Exact deterministic cookie encoding and database hash
- Nil entropy, immediate failure, and short-read failure return no partial
  material
- `generateSessionMaterial` at 100% statement coverage; auth package 96.8%

Risks / non-goals:

- The returned Go string is immutable and cannot be explicitly cleared. It is
  deliberately the browser credential and must remain confined to cookie
  construction.
- User-agent and IP risk metadata remain nullable in alpha; this unit does not
  invent weak unsalted fingerprint hashes.

### 2026-09-01 11:30 CDT — Add atomic identity and session query primitives

Commit: current commit; hash assigned by Git after commit

Affected files:

- `internal/store/queries/foundation.sql`
- `internal/store/db/foundation.sql.go`
- `migrations/schema_integration_test.go`
- `docs/implementation-spec.md`
- `docs/CHANGELOG.md`

Explanation:

Added deterministic sqlc primitives for the login transaction: a transaction-
scoped advisory lock keyed by separately hashed issuer and subject, approved-
profile-only user refresh, identity verification timestamp refresh, and exact
opaque-session insertion. The lock serializes concurrent creation of the same
verified identity so the later transaction boundary cannot create orphan users;
hash collisions only serialize unrelated identities.

Verification:

- PostgreSQL 17.10 proves the same identity lock contends while a different
  subject proceeds independently
- Profile refresh preserves a deliberately assigned local moderator role
- Verification timestamp, token/user-agent hashes, IP address, issued/seen/
  validated/expiry timestamps, and unrevoked session result are exact
- Deterministic no-remote sqlc generation and three fresh PostgreSQL 17.10 race/
  coverage runs; integration package 100% statement coverage

Risks / non-goals:

- Advisory hash collisions reduce concurrency only; identity equality still
  uses the full issuer/subject unique key.
- These are transaction primitives. The orchestration that chooses insert or
  refresh and commits the new session is the next unit.

### 2026-09-01 11:18 CDT — Exchange and verify OIDC codes exactly once

Commit: current commit; hash assigned by Git after commit

Affected files:

- `go.mod`
- `internal/auth/provider.go`
- `internal/auth/provider_test.go`
- `internal/auth/authorization_url.go`
- `internal/auth/authorization_url_test.go`
- `internal/auth/exchange.go`
- `internal/auth/exchange_test.go`
- `internal/auth/identity_claims.go`
- `internal/auth/identity_claims_test.go`
- `docs/implementation-spec.md`
- `docs/CHANGELOG.md`

Explanation:

Added the one-shot code-exchange boundary and retained the hardened HTTP client
inside the redacted discovered-provider value so token requests cannot silently
fall back to the default client. The exchange sends the recovered PKCE verifier
once, bounds the authorization code and raw ID token, verifies signature,
issuer, audience, expiry, nonce, and any advertised access-token hash, then
admits only the approved identity snapshot. Access, refresh, and ID tokens are
never returned or persisted. HTTPS issuers now also reject HTTP avatar claims.

Verification:

- Controlled ES256 discovery, Basic-auth token request, exact code/verifier/
  redirect/grant fields, cached JWKS verification, nonce, access-token hash,
  approved profile result, and exactly one exchange
- Non-OK response-body redaction; missing/wrong/oversized ID tokens; invalid
  signature, issuer, audience, expiry, nonce, subject, approved claims, access
  token, and access-token hash
- Nil/canceled contexts, cancellation during the token request, incomplete
  provider, empty/oversized/control/invalid-UTF-8 code, and malformed recovered
  nonce or verifier without a network request
- Controlled provider and authorization tests prove the retained HTTP-client
  invariant; exchange function 93.1%, auth package 96.7%

Risks / non-goals:

- Four branches remain invariant behind successful `x/oauth2`/`go-oidc`
  contracts: a nil token without error, a post-success raw-claims decode/nil
  map, and request-context cancellation appearing only after verifier failure.
  No fake token/verifier interfaces were introduced to manufacture them.
- Token strings are immutable Go strings and cannot be explicitly cleared;
  they remain function-local and are never formatted, logged, returned, or
  stored.
- Identity/session persistence and browser cookies remain later boundaries.

### 2026-09-01 11:08 CDT — Admit only approved verified identity claims

Commit: current commit; hash assigned by Git after commit

Affected files:

- `internal/auth/identity_claims.go`
- `internal/auth/identity_claims_test.go`
- `docs/implementation-spec.md`
- `docs/CHANGELOG.md`

Explanation:

Added the pure verified-claims admission boundary. It bounds immutable issuer
and subject coordinates, requires a bounded display name, accepts email only
when a strict `email_verified` boolean is true, and accepts only a bounded safe
canonical HTTP(S) avatar URL. Role, group, permission, and entitlement-shaped
claims are never decoded into the accepted result, so OIDC data has no path to
forum-local authorization state.

Verification:

- Exact accepted issuer, subject, display name, verified email, and avatar
- Absent optional fields and explicitly unverified email
- Empty, oversized, invalid UTF-8/control, null, malformed, and wrong-type
  coordinate/profile values; missing email-verification evidence
- Relative, oversized, credentialed, fragmented, empty-query, unsupported-
  scheme, and decoded-control avatar URLs
- Attempted administrator role and privileged groups are ignored structurally
- `validateIdentityClaims` at 100% statement coverage; auth package 97.3%

Risks / non-goals:

- Email is a nullable display/contact snapshot, never an identity key.
- This boundary accepts already verified token coordinates and claims. Signature,
  issuer, audience, expiry, nonce, and PKCE validation belong to the exchange
  boundary that calls it.

### 2026-09-01 11:02 CDT — Build exact initial OIDC authorization URLs

Commit: current commit; hash assigned by Git after commit

Affected files:

- `internal/auth/authorization_url.go`
- `internal/auth/authorization_url_test.go`
- `docs/CHANGELOG.md`

Explanation:

Added the initial authorization URL boundary. It accepts only a complete
discovered provider with the exact `openid profile email` scope set and a
strict, independent 256-bit state/nonce/PKCE material set. It delegates the
documented S256 challenge construction and query encoding to `x/oauth2`, places
state and nonce in the authorization request, and keeps the verifier and client
secret out of browser-visible state.

Verification:

- Exact authorization origin/path, response type, client ID, redirect URI,
  scopes, state, nonce, S256 challenge, and challenge method
- Explicit absence of the PKCE verifier and client secret
- Zero/incomplete providers; malformed, short, or repeated material values
- `initialAuthorizationURL` at 100% statement coverage; auth package 96.8%

Risks / non-goals:

- The provider discovery boundary owns endpoint validation. This function
  accepts only its fully initialized internal result rather than reopening URL
  policy.
- Browser redirect handling and persistence orchestration remain separate.

### 2026-09-01 10:57 CDT — Harden Authentik provider discovery

Commit: current commit; hash assigned by Git after commit

Affected files:

- `go.mod`
- `go.sum`
- `internal/auth/provider.go`
- `internal/auth/provider_test.go`
- `docs/implementation-spec.md`
- `docs/CHANGELOG.md`

Explanation:

Pinned `go-oidc` v3.20.0 and `x/oauth2` v0.36.0 after reading their discovery,
verification, remote-key, PKCE, and token-authentication contracts. Added exact
issuer discovery through a ten-second, redirect-refusing HTTP client with a
512 KiB response cap and a transport-level issuer-origin restriction retained by
later JWKS/token requests. Discovered authorization, token, and JWKS endpoints
must be canonical and same-origin. Supported signature algorithms and an
explicit confidential/public token-authentication style are required before
constructing the verifier and OAuth2 client.

Verification:

- Controlled confidential and public provider discovery with exact request,
  endpoints, scopes, callback, client fields, and redacted formatting
- Nil/canceled contexts, empty client ID, invalid callback/issuer encodings,
  issuer mismatch, unsafe/off-origin endpoints, unsupported signing algorithms,
  absent token-authentication methods, non-OK/malformed/oversized bodies,
  redirect refusal, transport failures, and mid-request cancellation
- Provider formatter and transport adapter at 100% statement coverage;
  `discoverOIDCProvider` at 97.2%; auth package 96.5%

Risks / non-goals:

- Two defensive branches are invariant behind the standard HTTP client and a
  successfully decoded `go-oidc` provider: a synthesized off-origin request and
  a later failure to decode the already retained discovery claims. They are not
  made injectable merely to manufacture coverage.
- Discovery constructs the provider boundary only. Authorization redirects,
  code exchange, claims, persistence, and cookies remain separate units.

### 2026-09-01 10:45 CDT — Consume and recover initial login attempts

Commit: current commit; hash assigned by Git after commit

Affected files:

- `internal/auth/login_consume.go`
- `internal/auth/login_consume_test.go`
- `docs/CHANGELOG.md`

Explanation:

Added the initial-login callback consumption boundary. It strictly validates
and hashes the fixed callback state, supplies an explicit microsecond-precision
consumption time to the atomic sqlc query, rejects non-login/session-bound rows,
revalidates the stored internal return path, and authenticates the protected
nonce and PKCE verifier. Any failure after the database update leaves the
attempt consumed and returns no partial material.

Verification:

- Exact original context, state hash, consumption time, initial-login metadata,
  protected material, and validated return path
- Nil dependencies, canceled context, malformed/noncanonical state, zero clock,
  missing/replayed attempt, database failure, wrong purpose, unexpected session,
  rejected/empty return path, and corrupt protection envelope
- `consumeInitialLogin` at 100% statement coverage; auth package 96.2%

Risks / non-goals:

- Browser-facing handlers must deliberately collapse these diagnostic errors;
  this internal function preserves causes for controlled logging and tests.
- This unit consumes and recovers an attempt only. Code exchange, ID-token
  verification, identity/session transaction, and cookie rotation remain later
  callback boundaries.

### 2026-09-01 10:38 CDT — Bind validated login creation to persistence

Commit: current commit; hash assigned by Git after commit

Affected files:

- `internal/auth/login_begin.go`
- `internal/auth/login_begin_test.go`
- `docs/implementation-spec.md`
- `docs/CHANGELOG.md`

Explanation:

Added the initial-login creation boundary. It rejects invalid dependencies or
canceled context, validates the raw return target before reading entropy,
normalizes the explicit clock to UTC PostgreSQL microsecond precision,
generates and protects one attempt, and synchronously inserts exact typed sqlc
parameters with a fixed five-minute lifetime. Browser material is returned only
after insertion succeeds.

Verification:

- Exact context, validated return path, purpose, nullable session, state hash,
  ciphertext, creation, and exclusive-expiry parameters
- Nil dependencies, canceled context, validation failure/empty result, zero
  clock, material/protection entropy failures, and insert failure
- Validators run before entropy; failed creation never calls insert or returns
  partial browser material
- `beginInitialLogin` at 100% statement coverage; auth package 95.2%

Risks / non-goals:

- The validator is an explicit authority dependency; this function does not
  independently know the configured base path.
- Go strings containing generated browser material remain live until garbage
  collection even when insertion fails; they are never formatted or logged.

### 2026-09-01 10:34 CDT — Correct unreleased change chronology

Commit: `6862d5ead4807644e9ec81229239eb3d8c78de7d`

Affected files:

- `docs/CHANGELOG.md`

Explanation:

Replaced inferred future heading times with the authoritative local Git commit
times for the eight shell/auth units from `f07eb57` through `92c77db`. Assigned
the now-known atomic-consume commit hash. No implementation or release behavior
changed.

Verification:

- Compared every corrected heading against `git log --date=local`
- Change-log order remains strictly newest-first

Risks / non-goals:

- Heading precision is minutes; commit timestamps retain seconds.

### 2026-09-01 10:30 CDT — Add atomic login-attempt persistence and consumption

Commit: `92c77dba2b6018aa07a24adaf69427ba064bb3c1`

Affected files:

- `internal/store/queries/auth.sql`
- `internal/store/db/auth.sql.go`
- `migrations/schema_integration_test.go`
- `docs/implementation-spec.md`
- `docs/CHANGELOG.md`

Explanation:

Added locally generated sqlc queries to insert protected OIDC login attempts and
atomically consume one current unconsumed state hash with `UPDATE ...
RETURNING`. The consume timestamp is explicit, creation is inclusive, expiry is
exclusive, and missing, future, expired, or replayed attempts all produce the
same no-row result.

Verification:

- Red-before-generation compile failure for both typed query methods
- Deterministic local-only sqlc generation and generated-file drift gate
- PostgreSQL 17.10 two-connection simultaneous consume yields exactly one
  complete correct row and one no-row miss
- Replay, expired, and future attempts all miss; only the winner is marked
  consumed and failed attempts remain unconsumed for cleanup
- Fresh schema integration package at 100% statement coverage and `make verify`

Risks / non-goals:

- These queries provide atomic storage semantics only. The auth service owns
  return-path validation, envelope recovery, attempt lifetime, and browser/
  session outstanding-attempt limits.
- Consumed rows are retained temporarily as replay evidence and removed later
  by bounded idempotent cleanup.

### 2026-09-01 10:22 CDT — Authenticate and recover login-attempt secrets

Commit: `2009847c095e0404f4a567266adf66b78c0c2a17`

Affected files:

- `internal/auth/login_protection.go`
- `internal/auth/login_protection_test.go`
- `internal/auth/login_recovery.go`
- `internal/auth/login_recovery_test.go`
- `docs/CHANGELOG.md`

Explanation:

Added the inverse login-attempt protection boundary. Recovery strictly decodes
the canonical browser state, compares its lookup hash in constant time,
validates both fixed envelope formats, derives field-specific keys, authenticates
the state-hash binding, and returns the nonce and PKCE verifier only after both
open successfully and remain distinct.

Verification:

- Exact protection-to-recovery round trip without formatting live material
- Empty, malformed, short, noncanonical, wrong, and hash-mismatched state
- Short, long, wrong-version, modified, and field-swapped envelopes
- Authenticated noncanonical plaintext and repeated recovered values
- Auth package coverage 94.1%; `recoverLoginMaterial` 95.8%

Risks / non-goals:

- The only uncovered recovery branches are rejection by `aes.NewCipher` for a
  fixed valid 32-byte key and by `cipher.NewGCM` for the standard AES block.
  Both are structurally unreachable under their standard-library contracts.
- Recovery authenticates transient material only. Database one-time
  consumption, expiry, and callback transaction policy remain separate.

### 2026-09-01 10:17 CDT — Protect login-attempt database secrets

Commit: `ca15f844876000c6294c076a1dc1b688dc616f1e`

Affected files:

- `internal/auth/login_protection.go`
- `internal/auth/login_protection_test.go`
- `docs/CHANGELOG.md`

Explanation:

Added fixed versioned AES-256-GCM envelopes for the OIDC nonce and PKCE
verifier. Separate HMAC-SHA-256 keys are derived from the 256-bit browser state
for each field, random 96-bit nonces are stored in each envelope, and the state
hash is authenticated as additional data. The database lookup stores only
SHA-256 of the canonical browser state.

Verification:

- Exact state lookup hash and two deterministic envelope SHA-256 fixtures
- Fixed version/72-byte envelope shape and absence of plaintext substrings
- Invalid, malformed, short, noncanonical, or repeated login material fails
  before reading protection entropy
- Nil, short, and failing nonce readers return zero protected material and
  preserve the entropy failure cause
- Auth package coverage 93.0%; `protectLoginMaterial` 91.5%

Risks / non-goals:

- Database protection is keyed by the short-lived 256-bit state. Disclosure of
  the live browser state compromises that attempt and already defeats its CSRF
  role; no long-lived encryption key or OIDC token is stored.
- Five branches are structurally unreachable under the fixed standard-library
  contracts: AES-256 key rejection, standard AES-GCM construction/overhead
  failure, and their two closure error propagations. Fake cipher injection
  merely to color those lines would weaken the mechanism.

### 2026-09-01 10:10 CDT — Reject repeated OIDC secret blocks

Commit: `c6283bf0a43e56ce55de9ddf9a80f7f76005db71`

Affected files:

- `internal/auth/login_material.go`
- `internal/auth/login_material_test.go`
- `docs/CHANGELOG.md`

Explanation:

Fail closed when the entropy source repeats any 256-bit state, nonce, or PKCE
verifier block. In particular, state must never equal the verifier exposed only
to the token endpoint, or the authorization request would disclose the PKCE
proof.

Verification:

- A 96-byte repeated source returns zero login material and an error
- Generator and auth package remain at 100% statement coverage
- 20 repeated race-enabled auth runs and `make verify`

Risks / non-goals:

- Exact-repeat detection does not attempt to estimate entropy quality. The
  production caller still must use `crypto/rand.Reader`.

### 2026-09-01 10:06 CDT — Generate fail-closed OIDC login material

Commit: `5c11449dda4abf259c672ba7d03724745739070c`

Affected files:

- `internal/auth/login_material.go`
- `internal/auth/login_material_test.go`
- `docs/CHANGELOG.md`

Explanation:

Added one injected-entropy generator for independent 256-bit state, nonce, and
PKCE verifier values. All values use unpadded base64url encoding, matching the
PKCE verifier alphabet and producing 43-byte browser parameters. Entropy
failure returns no partial material, and the mutable source buffer is cleared
before return.

Verification:

- Exact deterministic separation of all three 32-byte entropy blocks
- Base64url decoding proves 256 bits per value and 43-byte encoded length
- Nil, short, and failing readers return zero material and preserve the cause
- Production function and package at 100% statement coverage

Risks / non-goals:

- The generated strings are intentionally live secrets until the login attempt
  is consumed; Go strings cannot be reliably zeroed.
- Database hashing/protection and one-time persistence are separate subsequent
  boundaries.

### 2026-09-01 10:02 CDT — Validate configured internal return paths

Commit: `d214516c473f414e26f5f055c416b0f6b6903575`

Affected files:

- `internal/httpui/url_builder.go`
- `internal/httpui/url_builder_test.go`
- `docs/CHANGELOG.md`

Explanation:

Added the application half of the login return-path boundary. One validated URL
builder now accepts only canonical request URIs within its exact configured
application subtree, including root deployments and alternate nested prefixes.
The validator returns the original bytes only after rejecting external or
network authorities, fragments, traversal/empty segments, encoded separators,
backslashes, decoded controls, noncanonical path/query encoding, and values
outside the database byte bound.

Verification:

- Root, `/bb`, alternate nested, Unicode, route, trailing-slash, and sorted
  query success cases
- External, sibling-prefix, traversal, repeated-separator, encoded separator/
  control/backslash, raw Unicode, noncanonical query, fragment, empty-query,
  malformed escape, zero-builder, and overlong rejection cases
- `ValidateReturnPath` at 100% statement coverage
- 20 repeated race-enabled HTTP UI runs and `make verify`

Risks / non-goals:

- A valid return path conveys navigation only. It grants no identity,
  authorization, or CSRF authority.
- Individual destination handlers still validate their own query values.

### 2026-09-01 09:56 CDT — Remove the base-path literal from login-attempt storage

Commit: `09f9b612ff0eaea0fceaa5739cbb08a1f92f38ec`

Affected files:

- `migrations/000001_identity_and_sessions.sql`
- `migrations/schema_integration_test.go`
- `docs/implementation-spec.md`
- `docs/CHANGELOG.md`

Explanation:

Corrected the unreleased initial login-attempt constraint so it accepts an
internal browser path for root or configured-prefix deployments instead of
hard-coding the temporary `/bb` development prefix. PostgreSQL rejects empty,
relative, network-path, absolute-URL, backslash, fragment, control-character,
and overlong values. The application remains responsible for proving the exact
configured base-path containment and canonical encoding before insertion.

Verification:

- PostgreSQL 17.10 fresh migration accepts `/`, `/bb/`, and an alternate nested
  prefix with a query
- PostgreSQL rejects each unsafe structural class without relying on an HTTP
  handler
- Existing migration/schema and full race/coverage verification

Risks / non-goals:

- This changes an initial migration only because no alpha.1 tag or deployed
  migration ledger exists. Once released, migration bytes are immutable.
- Database structure is not a substitute for application base-path validation;
  that boundary is implemented before login-attempt persistence.

### 2026-09-01 09:47 CDT — Render the responsive base-path-safe public shell

Commit: `f07eb57612aac7995901b3d3e38207ccdaf0c6a4`

Affected files:

- `.node-version`, `.npmrc`, `package.json`, `package-lock.json`
- `go.mod`, `go.sum`, `Makefile`
- `assets/styles/app.css`
- `cmd/forum/main.go`
- `internal/httpui/handler.go`, `internal/httpui/handler_test.go`
- `internal/httpui/render.go`, `internal/httpui/render_test.go`
- `internal/httpui/route_pattern.go`, `internal/httpui/route_pattern_test.go`
- `internal/httpui/shell.templ`, `internal/httpui/shell_templ.go`
- `internal/httpui/static.go`, `internal/httpui/static_test.go`
- `internal/httpui/static/app-1.0.0-alpha.1.css`
- `internal/httpui/static/htmx-2.0.10.min.js`
- `internal/httpui/url_builder.go`, `internal/httpui/url_builder_test.go`
- `internal/httpui/view.go`, `internal/httpui/view_test.go`
- `README.md`, `docs/implementation-spec.md`, `docs/CHANGELOG.md`

Explanation:

Added the compiled Templ public document and fragment shell, responsive
Tailwind layout, keyboard-visible focus treatment, skip link, breadcrumbs,
navigation, canonical URL, custom full/fragment `404`, and immutable embedded
Tailwind and HTMX assets. Chi now owns the internal route table while an
explicit bridge preserves the existing method-qualified `request.Pattern`
observability contract across Chi's request clone. The forum process constructs
all browser URLs from immutable configuration before opening PostgreSQL.
The browser policy wraps the full request-ID/logging/recovery chain so bounded
panic responses retain the same CSP and defensive headers.

Verification:

- Read current templ, Chi, Tailwind, HTMX, npm, and Go tool contracts before
  pinning exact versions
- Red-before-green page-view, renderer, static-handler, router, cookie-path,
  and route-pattern tests
- Root and alternate-prefix full pages, HTMX fragments, history restoration,
  health, CSS, JavaScript, `404`, and standards-compliant default `405`
- Rendered-link scan covering `href`, `src`, `action`, `hx-get`, and `hx-post`
  proves no application URL escapes the configured alternate prefix
- Panic-recovery response retains the exact browser security boundary after
  recovery clears unsafe application-added headers
- Exact CSS and HTMX SHA-256 checks; Tailwind scans only `.templ` sources
- Valid restrictive HTMX JSON; no inline script/style dependency
- `npm audit` reported zero known vulnerabilities across the locked graph
- Touched handwritten HTTP UI functions at 100% statement coverage except
  three structurally unreachable hard-coded asset-path error returns in
  `newPageView`; generated Templ code brings the package aggregate to 80.6%
- Actual forum process connected to PostgreSQL 17.10, served full/fragment/CSS/
  `404` responses, logged `GET /` and the static matched pattern, and stopped
  cleanly on SIGTERM
- `make verify` from a clean generated state

Risks / non-goals:

- The area index is deliberately an honest empty-state shell; database-backed
  reads arrive in A1-06.
- Sign-in remains non-interactive until A1-04. No placeholder authentication
  route or fake session is exposed.
- npm permits integrity-locked remote tarballs because Tailwind publishes its
  WASI fallback that way; package scripts and Git dependencies remain disabled.

### 2026-09-01 09:08 CDT — Make HTMX representation selection cache-safe

Commit: `db5eb33aac5c72bb2e2b12c6b4d6fd7529f2bb1a`

Affected files:

- `internal/httpui/response_mode.go`
- `internal/httpui/response_mode_test.go`
- `docs/implementation-spec.md`
- `docs/CHANGELOG.md`

Explanation:

Added the shared full-page/fragment selector using HTMX's documented exact
`HX-Request: true` contract. History restoration always receives a full page,
and every representation-varying response marks both request headers in
`Vary` without replacing an existing cache key.

Verification:

- Read HTMX 2.0.10's current request-header, history-restoration, response
  handling, and cache variation documentation
- Red-before-green response-mode compile failure
- Ordinary, exact HTMX, noncanonical, history-restore, and explicit non-history
  request cases
- Existing `Vary` value preserved before both representation headers
- `go test -mod=readonly -race -cover ./internal/httpui` at 100% statement
  coverage
- 20 repeated race-enabled HTTP UI package runs
- `make verify`

Risks / non-goals:

- `HX-Request` changes representation only; it conveys no identity,
  authorization, CSRF, or validation authority.
- The page-level HTMX configuration that swaps `422` forms and requests full
  history restoration remains part of the layout unit.

### 2026-09-01 09:01 CDT — Install the fixed browser security boundary

Commit: `143ec1ac64e879fa851b686cabe648ec358687eb`

Affected files:

- `internal/httpui/security_headers.go`
- `internal/httpui/security_headers_test.go`
- `internal/httpui/handler.go`
- `internal/httpui/handler_test.go`
- `docs/implementation-spec.md`
- `docs/CHANGELOG.md`

Explanation:

Wrapped the complete application router with one fixed defensive browser
policy. The CSP permits only same-origin scripts, styles, fonts, connections,
and manifests, same-origin plus data images, same-origin form actions, and no
default, base, frame-ancestor, or object sources. The boundary also installs
content-sniffing, framing, referrer, cross-origin isolation, origin-agent, and
permissions headers before any handler writes a response.

Verification:

- Red-before-green policy-constructor compile failure
- Policy visible inside the delegated handler before status output
- Exact fixed value for every admitted defensive header
- HSTS explicitly absent because TLS transport policy is Caddy-owned
- Successful health, failed readiness, unknown route, and method-not-allowed
  responses all retain the CSP
- `go test -mod=readonly -race -cover ./internal/httpui` at 100% statement
  coverage
- 20 repeated race-enabled HTTP UI package runs
- `make verify`

Risks / non-goals:

- The fixed policy intentionally rejects inline script and style. Templates and
  HTMX integration must use versioned same-origin assets.
- HSTS, TLS, and the external `/bb` redirect remain Caddy responsibilities.

### 2026-09-01 08:52 CDT — Bind relative and canonical URLs to one authority

Commit: `9d86882ca2fab692478e26e287fb4bdfece34abc`

Affected files:

- `internal/httpui/url_builder.go`
- `internal/httpui/url_builder_test.go`
- `docs/CHANGELOG.md`

Explanation:

Extended the browser URL builder to require a validated, path-consistent
`PUBLIC_BASE_URL` and `BASE_PATH` pair. It now produces prefix-safe relative
paths, canonical absolute URLs that preserve encoded path-segment boundaries,
and deterministically escaped query strings without consulting request headers.

Verification:

- Red-before-green constructor, absolute-URL, and query-path compile failures
- Root, `/bb`, alternate nested, Unicode, and hostile path-segment cases
- Missing, mismatched, credential-bearing, and query-bearing public authority
  rejection
- Uninitialized public builder fails closed instead of fabricating root URLs
- Deterministically sorted, repeated, and escaped query values
- Corrupted internal builder and ambiguous segment failures
- `go test -mod=readonly -race -cover ./internal/httpui` at 100% statement
  coverage
- 20 repeated race-enabled HTTP UI package runs
- `make verify`

Risks / non-goals:

- Route handlers remain responsible for query-length and pagination bounds.
- The builder creates URLs; it does not authorize whether a caller may disclose
  the resource named by a URL.

### 2026-09-01 08:38 CDT — Add the one-shot migration command

Commit: `beb42175c42f42c4b829eb3c1f0ee9fdc9b2e142`

Affected files:

- `cmd/migrate/main.go`
- `cmd/migrate/main_test.go`
- `internal/config/database_connection_test.go`
- `README.md`
- `docs/CHANGELOG.md`

Explanation:

Added `cmd/migrate` as the visible one-shot schema-maintenance entry point. It
loads only the migration database URL, applies the exact SQL files embedded in
the same source release, owns process-signal cancellation, opens one direct
connection through the project runner, and never starts HTTP, constructs a
pool, or retries an unknown database outcome.

Verification:

- Red-before-green compile failure before the command runner existed
- Exact context, parsed direct connection configuration, and release
  filesystem forwarding
- Nil dependency and cancellation-before-side-effect failures
- Malformed credential-bearing configuration failure without secret exposure
- Failure diagnostics never format a potentially credential-bearing pgx
  configuration
- Migration-runner cause preservation
- Tested `run` function at 100% statement coverage
- 20 repeated race-enabled command-package runs
- Real command execution against a fresh PostgreSQL 17.10 database, exact
  four-migration head, idempotent second execution, and disposable cleanup
- `make verify`

Risks / non-goals:

- Forward migrations are privileged release code. The command intentionally
  has no automatic rollback, fake down migration, pool, or retry path.
- Deployment role grants and secret injection remain deployment-owned.

### 2026-09-01 08:25 CDT — Isolate migration database configuration

Commit: `cd30ce6e92013ad6b133aa57c5cc152ed316c0ea`

Affected files:

- `internal/config/database_connection.go`
- `internal/config/database_connection_test.go`
- `docs/CHANGELOG.md`

Explanation:

Added a migration-specific configuration boundary that reads only
`DATABASE_URL`, accepts the same pool-compatible URL grammar as the service,
returns a copied pgx direct-connection configuration, and pins connection
establishment to five seconds. It does not require HTTP or OIDC settings to run
schema maintenance.

Verification:

- Red-before-green compile failure before `LoadDatabaseConnectionConfig`
  existed
- Valid host, port, database, user, pool-parameter compatibility, and timeout
- Nil lookup, missing/empty value, and malformed secret-bearing URL failures
- Secret absent from every failure string
- `go test -mod=readonly -race -cover ./internal/config` at 100% statement
  coverage
- 20 repeated race-enabled config-package runs
- `make verify`

Risks / non-goals:

- The returned pgx structure necessarily contains credentials and must never be
  formatted or logged. Its sole consumer is the migration command boundary.

### 2026-09-01 08:20 CDT — Bound repository transactions

Commit: `53328c37c56c30874a395738ba905eb9f93ee53f`

Affected files:

- `internal/store/transaction.go`
- `internal/store/transaction_test.go`
- `internal/store/queries/foundation.sql`
- `internal/store/db/foundation.sql.go`
- `migrations/schema_integration_test.go`
- `docs/CHANGELOG.md`

Explanation:

Added `store.WithinTx`, which binds generated queries to one pgx transaction,
commits once, performs bounded cancellation-detached rollback on every earlier
failure, joins cleanup failures, and never retries an unknown commit outcome.
Foundation inserts now use the validated identity timestamp for both creation
and verification columns, avoiding impossible client-time/default-time races.

Verification:

- Red-before-green compile failure before `WithinTx` existed
- Successful action, invalid inputs, begin/nil transaction, callback, commit,
  rollback, joined-failure, and canceled-caller paths
- `WithinTx` itself at 100% statement coverage; store package at 95.6% because
  the pre-existing real pool constructor retains its documented integration
  branches
- PostgreSQL 17.10 transaction creates a user plus external identity atomically
  through generated queries
- PostgreSQL 17.10 callback failure leaves no user row
- Generated query drift regenerated and checked by `make verify`
- 20 repeated race-enabled store-package runs

Risks / non-goals:

- The callback may execute external side effects only if its caller explicitly
  owns that non-transactional failure problem. This wrapper governs PostgreSQL
  work and does not pretend to make networks transactional.

### 2026-09-01 08:14 CDT — Generate typed foundation queries

Commit: `0b1e34fe74dbd0f6ebd30964b44a62a733f606ad`

Affected files:

- `sqlc.yaml`
- `internal/store/queries/foundation.sql`
- `internal/store/db/db.go`
- `internal/store/db/models.go`
- `internal/store/db/foundation.sql.go`
- `migrations/schema_integration_test.go`
- `Makefile`
- `docs/CHANGELOG.md`

Explanation:

Configured pinned sqlc v1.31.1 for local PostgreSQL analysis and pgx/v5
generation. Generated schema models plus the first typed identity, governance,
and active-administrator queries. `make generate` is explicit; `make verify`
regenerates with `--no-remote` and rejects working-tree or untracked output
drift before compiling and testing.

Verification:

- Two consecutive `go tool sqlc generate --no-remote` runs produced identical
  SHA-256 values for every generated file
- Full race-enabled Go test suite
- PostgreSQL 17.10 round trips for governance cardinality, active administrator
  count, and external-identity lookup through generated queries
- Fresh-schema integration test remains at 100% migrations-package coverage
- `make verify`, including generation-drift checks

Risks / non-goals:

- Generated code is committed and never hand-edited. This unit provides typed
  primitives only; transaction ownership and repository policy stay in
  handwritten code.
- Database-backed sqlc analysis and managed/remote generation remain disabled.

### 2026-09-01 08:08 CDT — Create the alpha database schema

Commit: `3707087ffe7237fcb4c19496aebb07442f4f0223`

Affected files:

- `migrations/000001_identity_and_sessions.sql`
- `migrations/000002_groups_and_areas.sql`
- `migrations/000003_topics_posts_and_reads.sql`
- `migrations/000004_reports_and_audit.sql`
- `migrations/files.go`
- `migrations/files_test.go`
- `migrations/schema_integration_test.go`
- `docs/CHANGELOG.md`

Explanation:

Added four contiguous forward-only migrations compiled into the release:
identity/session state; groups/areas; topics/posts/read state; and
reports/moderation audit. Closed values, lengths, timestamps, identities,
singletons, target cardinality, post numbering, and audit structure are
database constraints. Row-locking triggers serialize area/group visibility;
deferred triggers prove topic/post pointers and counters at commit; published
area slugs and post topic/number identities are immutable.

Verification:

- Red-before-green embed API test before `migrations.Files` existed
- Embedded filesystem contains only the four exact ordered SQL files
- Fresh public `migration.Apply` into a disposable PostgreSQL 17.10 database
- Exact four-row migration head and one governance singleton
- Invalid roles, visibility, posting modes, duplicate external identities,
  malformed session hashes, duplicate post numbers, invalid report targets,
  invalid audit actors, and inconsistent topic counters rejected
- Area group mappings require group visibility under row locks; visibility and
  slug drift are rejected
- Topic plus first post and a reply commit with deferred circular integrity;
  partial counter corruption rolls back
- `go test -mod=readonly -race -cover ./migrations` with 100% statement coverage
- PostgreSQL integration coverage for the migrations package at 100%
- `make verify`

Risks / non-goals:

- Runtime/migration roles and grants remain deployment-owned configuration;
  application repositories must still append audit rows in the same mutation
  transaction. These migrations do not create cluster roles.
- Search indexes and later feature schema are added only with reviewed forward
  migrations; no fake down path exists.

### 2026-09-01 07:57 CDT — Expose the migration runner

Commit: `6b7d562d0d94413f3398ebbe6dafb2f9575a4aea`

Affected files:

- `internal/migration/api.go`
- `internal/migration/api_test.go`
- `internal/migration/coordinator_integration_test.go`
- `docs/CHANGELOG.md`

Explanation:

Exposed `migration.Apply` as the only production entry point. It accepts a
configuration produced by `pgx.ParseConfig`, opens one direct connection with
`pgx.ConnectConfig`, delegates to the tested owner/coordinator chain, and never
creates a pool or retries an unknown transaction outcome. The adapter
normalizes pgx's typed nil pointer on failed connection attempts before it
crosses the ownership interface.

Verification:

- Red-before-green compile failure before `Apply` existed
- Parsed pgx configuration with a deterministic injected dial failure
- Nil public context/config rejection
- Local race-enabled package coverage at 99.5%; the only local gap is the
  successful real-connection adapter return
- PostgreSQL 17.10 integration suite now enters through public `Apply`, proving
  the successful adapter, connection ownership, lock, migration, unlock, and
  close path at 100% migration-package statement coverage
- 20 repeated race-enabled package runs
- `make verify`

Risks / non-goals:

- `configured` must come from `pgx.ParseConfig`, matching pgx's documented
  precondition. The migration command wiring and embedded release files remain
  subsequent units.

### 2026-09-01 07:51 CDT — Own the migration connection lifetime

Commit: `d0c6daab823520411da17e2c3af20579fbb4c136`

Affected files:

- `internal/migration/owner.go`
- `internal/migration/owner_test.go`
- `internal/migration/coordinator_test.go`
- `docs/CHANGELOG.md`

Explanation:

Added the private connection owner around the release coordinator. It validates
inputs before connecting, owns any non-nil connection even when the connector
also returns an error, preserves cancellation identity, and always closes on a
five-second cancellation-detached context. Close failure is joined with the
connection or coordinator failure rather than hiding either.

Verification:

- Red-before-green compile failure before `applyWithConnector` existed
- Exact context/config forwarding and cancellation during release work
- Nil/canceled inputs, connect failure, cancellation during connect,
  inconsistent nil connection, coordinator failure, and close failure
- Proof that close begins uncanceled after caller cancellation
- `go test -mod=readonly -race -cover ./internal/migration` with 100% statement
  coverage
- 20 repeated race-enabled package runs
- `make verify`

Risks / non-goals:

- This unit accepts an injected private connector so ownership failures are
  deterministic in unit tests. The next unit is the public wrapper pinned to
  `pgx.ConnectConfig`; callers never select a connector.

### 2026-09-01 07:45 CDT — Coordinate a migration release

Commit: `8997208617d606d672653181ccdcdbb10ce5cb80`

Affected files:

- `internal/migration/coordinator.go`
- `internal/migration/coordinator_test.go`
- `internal/migration/coordinator_integration_test.go`
- `docs/CHANGELOG.md`

Explanation:

Composed the migration primitives around one already-open dedicated
connection. Release files load before lock acquisition; the locked section
bootstraps and attests the ledger, validates the applied prefix, executes the
pending suffix in order, and re-reads exact head before unlock. An already
current release avoids the second attestation, history query, and all
transactions. A changed release re-attests the ledger after privileged SQL and
before trusting final history.

Verification:

- Red-before-green compile failure before `applyRelease` existed
- Fresh/current paths and every validation, lock, bootstrap, attestation,
  history, drift, execution, final-attestation, final-head, and unlock failure
  path
- `go test -mod=readonly -race -cover ./internal/migration` with 100% statement
  coverage and 20 repeated race-enabled runs
- PostgreSQL 17.10 fresh apply, idempotent reapply, changed applied bytes, and
  unknown database-version rejection
- PostgreSQL 17.10 concurrent runner proof: two dedicated sessions both return
  success while exactly two migrations are recorded once
- PostgreSQL integration run with the `integration` build tag and 100%
  migration-package statement coverage
- `make verify`

Risks / non-goals:

- This private coordinator does not open or close the connection. The public
  owner wrapper is the next unit and must close its dedicated connection on
  every exit, including unlock or commit uncertainty.

### 2026-09-01 07:35 CDT — Attest the migration ledger schema

Commit: `76d463df47238edc378a74202fcb96d91f427022`

Affected files:

- `internal/migration/attest.go`
- `internal/migration/attest_test.go`
- `internal/migration/attest_integration_test.go`
- `internal/migration/lock.go`
- `docs/CHANGELOG.md`

Explanation:

Added an exact PostgreSQL 17 catalog attestation for the migration ledger.
Before history is trusted, the runner can now prove the permanent ordinary
table, four ordered column types/nullability/defaults, three named validated
constraints and definitions, no inheritance, no row security, and no external
triggers or rewrite rules. This closes the false assurance left by
`CREATE TABLE IF NOT EXISTS`.

Verification:

- Red-before-green compile failure before `attestHistoryTable` existed
- Exact query plus nil/canceled/query-failure/catalog-mismatch unit paths
- PostgreSQL 17.10 acceptance of the exact created table
- PostgreSQL 17.10 rejection after removing the digest constraint, adding an
  extra column, changing the timestamp default, or enabling row security
- PostgreSQL 17.10 rejection of an insert-suppressing rewrite rule
- `go test -mod=readonly -race -cover ./internal/migration` with 100% statement
  coverage
- PostgreSQL integration run with the `integration` build tag and 100%
  migration-package statement coverage
- 20 repeated race-enabled package runs
- `make verify`

Risks / non-goals:

- The attestation is intentionally pinned to PostgreSQL 17.10 catalog output.
  Supporting another major version requires rerunning and admitting the full
  migration, constraint, concurrency, and readiness evidence.

### 2026-09-01 07:28 CDT — Apply one migration atomically

Commit: `b6e1ceaaf983717ab6c1cd9cc5a568ee9d5e5d51`

Affected files:

- `internal/migration/apply_one.go`
- `internal/migration/apply_one_test.go`
- `internal/migration/apply_one_integration_test.go`
- `docs/implementation-spec.md`
- `docs/CHANGELOG.md`

Explanation:

Added the one-file transactional executor. It revalidates the private loaded
identity and digest, begins a PostgreSQL transaction, executes the exact SQL,
inserts its version/name/SHA-256 record, and commits. Every pre-commit failure
receives a bounded cancellation-detached rollback. Commit failure is reported
as outcome-unknown and explicitly requires ledger inspection before retry.

Verification:

- Red-before-green compile failure before `applyMigration` existed
- Exact SQL/record order and identity arguments
- Invalid context, connection, version, filename, content, and digest
- Begin, SQL, ledger, commit, and rollback failures including joined causes
- PostgreSQL 17.10 proof that successful DDL and its ledger row commit together
- PostgreSQL 17.10 proof that partial multi-statement DDL and DDL followed by a
  duplicate ledger record both roll back without residue
- `go test -mod=readonly -race -cover ./internal/migration` with 100% statement
  coverage
- PostgreSQL integration run with the `integration` build tag and 100%
  migration-package statement coverage
- 20 repeated race-enabled package runs
- `make verify`

Risks / non-goals:

- Migration SQL is trusted reviewed release code and may not contain explicit
  transaction control; the runner cannot sandbox arbitrary privileged SQL.
- No automatic retry occurs after a commit error because its outcome may be
  unknown.

### 2026-09-01 07:20 CDT — Serialize migration sessions

Commit: `3d9f08a477844ce4c5ac83569563a1076783a82f`

Affected files:

- `internal/migration/lock.go`
- `internal/migration/lock_test.go`
- `internal/migration/lock_integration_test.go`
- `docs/CHANGELOG.md`

Explanation:

Added a project-specific PostgreSQL session advisory lock around migration
actions. Acquisition and release use the same connection and fixed signed key.
Release receives a five-second cancellation-detached context, reports false or
failed unlocks, and joins cleanup failure with any action failure. The caller
must close the dedicated connection on every return, eliminating leaked locks
even when the unlock outcome is ambiguous.

Verification:

- Red-before-green compile failure before `withMigrationLock` existed
- Exact acquisition/release SQL and stable key
- Nil/canceled inputs, acquisition failure, action failure, false unlock,
  unlock failure, joined causes, and cancellation-detached cleanup
- PostgreSQL 17.10 proof that a second session cannot take the held key and can
  take it immediately after release
- `go test -mod=readonly -race -cover ./internal/migration` with 100% statement
  coverage
- PostgreSQL integration run with the `integration` build tag and 100%
  migration-package statement coverage
- 20 repeated race-enabled package runs
- `make verify`

Risks / non-goals:

- This unit does not own the connection. The coordinator must use a dedicated
  connection, close it on every exit, and never return a possibly locked
  session to a pool.

### 2026-09-01 07:14 CDT — Bootstrap the migration history ledger

Commit: `25f8d2655803a3f172fdb83cc42d8783a3ba4dee`

Affected files:

- `internal/migration/bootstrap.go`
- `internal/migration/bootstrap_test.go`
- `internal/migration/bootstrap_integration_test.go`
- `docs/CHANGELOG.md`

Explanation:

Added the idempotent PostgreSQL migration-ledger definition with a positive
bigint primary-key version, non-null name, exact 32-byte digest constraint, and
database-generated application timestamp. The operation accepts pgx
connections or transactions through a private execution contract and requires
the caller to hold the migration advisory lock.

Verification:

- Red-before-green compile failure before `ensureHistoryTable` existed
- Exact statement and nil context/executor/execution-failure unit paths
- Idempotent creation on PostgreSQL 17.10
- Successful row/default round trip and rejection of nonpositive version, null
  name, short digest, and duplicate version
- `go test -mod=readonly -race -cover ./internal/migration` with 100% statement
  coverage
- PostgreSQL integration run with the `integration` build tag and 100%
  migration-package statement coverage
- 20 repeated race-enabled package runs
- `make verify`

Risks / non-goals:

- `CREATE TABLE IF NOT EXISTS` creates but does not attest a pre-existing table
  definition. Schema attestation and advisory-lock ownership remain required in
  the executor coordinator.

### 2026-09-01 07:09 CDT — Bound applied migration history queries

Commit: `cfcf74a849c402274152047d72173f7995415e81`

Affected files:

- `internal/migration/query.go`
- `internal/migration/query_test.go`
- `docs/CHANGELOG.md`

Explanation:

Added the exact ordered PostgreSQL history query. Its row limit is the loaded
release count plus one: enough to prove that the database contains an unknown
version, but unable to scan an inconsistent history without a release-derived
bound. Query execution stays behind a private one-method pgx contract used by
real connections and deterministic tests.

Verification:

- Red-before-green compile failure before `readAppliedMigrations` existed
- Exact SQL, ascending version order, and release-plus-one argument
- Nil context, nil connection, empty/overflowing count, and query failure paths
- `go test -mod=readonly -race -cover ./internal/migration` with 100% statement
  coverage
- 20 repeated race-enabled package runs
- `make verify`

Risks / non-goals:

- The migration state table is created by the executor bootstrap unit. Locking
  and transactional execution remain subsequent units.

### 2026-09-01 07:04 CDT — Read applied migration identities

Commit: `b76bc546b8d411dc1ba90388f981f223fa985342`

Affected files:

- `internal/migration/rows.go`
- `internal/migration/rows_test.go`
- `docs/CHANGELOG.md`

Explanation:

Added the result-set boundary for applied migration identities. It owns and
closes the supplied `pgx.Rows`, copies each driver-owned SHA-256 buffer into an
immutable fixed-size value, rejects malformed digest lengths, and preserves
iteration and scan failures. Query construction and ordering remain outside
this single-function unit.

Verification:

- Red-before-green compile failure before `scanAppliedMigrations` existed
- Exact two-row decode and proof that driver-buffer mutation cannot alter the
  returned digest
- Nil rows, scan failure, iteration failure, and short/long digest failures
- `go test -mod=readonly -race -cover ./internal/migration` with 100% statement
  coverage
- 20 repeated race-enabled package runs
- `make verify`

Risks / non-goals:

- The caller must issue the history query ordered by version. The query,
  advisory lock, and transaction execution remain subsequent units.

### 2026-09-01 06:57 CDT — Reject applied migration drift

Commit: `fc23d6d4151a2c303ebcaae74751d940b70a8565`

Affected files:

- `internal/migration/drift.go`
- `internal/migration/drift_test.go`
- `docs/CHANGELOG.md`

Explanation:

Added the pure migration-history oracle. Applied PostgreSQL rows must be an
exact contiguous prefix of loaded files by version, filename, and SHA-256. A
gap, rename, changed byte sequence, or database version unknown to the release
fails closed. The pending result is a slice of the already loaded file set, so
the check performs no SQL-content copies or per-row allocations.

Verification:

- Red-before-green tests before `pendingMigrations` existed
- Fresh, partially applied, and fully applied histories
- Version gap, rename, changed digest, unknown database version, and empty
  release set failures
- `go test -mod=readonly -race -cover ./internal/migration` with 100% statement
  coverage
- `make verify`

Risks / non-goals:

- This unit compares already loaded state only. Reading applied rows, locking,
  and transaction execution remain the next migration units.

### 2026-09-01 06:52 CDT — Pin SQL generation and migration identity

Commit: `aa342147f525e260c6b5f1616979a9f5d76693bd`

Affected files:

- `go.mod`
- `go.sum`
- `internal/migration/loader.go`
- `internal/migration/loader_test.go`
- `docs/implementation-spec.md`
- `docs/CHANGELOG.md`

Explanation:

Pinned sqlc v1.31.1 as a build-only Go tool after reviewing its version-2
configuration, pgx/v5 generation, migration-directory parsing, local analyzer,
and transaction contracts. Rejected tern v2.4.3 because its single version row
cannot detect edited applied SQL. Added the first project-owned migration unit:
a strict flat loader for contiguous six-digit lowercase filenames, exact
SHA-256 content identities, nonempty SQL, and a one-MiB file ceiling.

Verification:

- Red-before-green loader tests before `loadMigrations` existed
- Boundary tests at one MiB minus one, exactly one MiB, one MiB plus one, and
  four MiB
- Missing, duplicate, malformed, empty, nested, read-failure, metadata-failure,
  and dishonest-filesystem cases
- `go test -mod=readonly -race -cover ./internal/migration` with 100% statement
  coverage
- `go tool sqlc version` reports `v1.31.1`
- Production dependency traversal contains no sqlc compiler packages
- `make verify`

Risks / non-goals:

- The sqlc compiler has a large build-only dependency graph and an expensive
  first compilation. It is not linked into the forum binary.
- This unit loads and identifies migrations only. PostgreSQL locking, drift
  comparison, transactional execution, schema SQL, and the migration command
  remain the next A1-02 units.

### 2026-09-01 06:39 CDT — Wire executable PostgreSQL ownership

Commit: `30dc5a96c190ec0b859b64cca160bfd193f252f8`

Affected files:

- `cmd/forum/main.go`
- `cmd/forum/main_test.go`
- `docs/implementation-spec.md`
- `docs/CHANGELOG.md`

Explanation:

Wired the admitted pgx configuration and pool lifecycle into executable
startup. The service now refuses to bind its HTTP listener until PostgreSQL has
passed the bounded initial round trip. Once opened, the pool is closed exactly
once after graceful service stop and on every later startup or listener
failure. The single injected pool-factory seam keeps unit tests deterministic;
its returned errors are redacted, and inconsistent pool-plus-error or nil-pool
results fail closed without leaking ownership.

Verification:

- Red-before-green executable tests before the pool dependency existed
- Pool open failure, returned-pool cleanup, nil result, cancellation during and
  after open, listener failure, and graceful shutdown ownership tests
- `go test -mod=readonly -race -cover ./cmd/forum` with 91.3% statement coverage
  of the testable `run` function; the process-terminating `main` wrapper remains
  deliberately outside in-process coverage
- `make verify`

Risks / non-goals:

- The live pool is not yet passed to repositories because migrations and
  queries do not exist.
- Readiness remains deliberately false until exact migration-head and
  governance singleton checks are wired.

### 2026-09-01 06:32 CDT — Prove PostgreSQL pool startup

Commit: `6b02472fc96dc8ec33bc389722767a0f5d5b8cd6`

Affected files:

- `internal/store/pool.go`
- `internal/store/pool_test.go`
- `internal/store/pool_integration_test.go`
- `docs/implementation-spec.md`
- `docs/release-operations.md`
- `docs/CHANGELOG.md`

Explanation:

Added the PostgreSQL pool ownership boundary. It accepts only a configuration
created through pgx's parser, rejects missing or canceled dependencies, proves
one initial database round trip within five seconds, closes the pool on every
failed startup check, and returns ownership only after success. PostgreSQL 17
is the alpha support target, with PostgreSQL 17.10 and its exact container
digest recorded as the integration reference.

Verification:

- Red-before-green dependency tests before `OpenPool` existed
- pgx v5.10.0 `NewWithConfig`, `Ping`, `Close`, and pool source inspected
- `go test -mod=readonly -race -cover ./internal/store`
- PostgreSQL 17.10 integration tests for successful connection/version proof,
  connection-failure redaction, invalid configuration, and cancellation with
  100% statement coverage of `internal/store`
- `make verify`

Risks / non-goals:

- The executable does not own this pool yet; startup/shutdown wiring is the
  next production unit.
- Migration head, schema invariants, governance cardinality, and readiness
  remain deliberately unimplemented and fail closed.

### 2026-09-01 06:18 CDT — Bound PostgreSQL pool configuration

Commit: `7d0bbdc2caecb17ed06f7b1bdcec103d9c4a43de`

Affected files:

- `go.mod`
- `go.sum`
- `internal/config/database_pool.go`
- `internal/config/database_pool_test.go`
- `docs/implementation-spec.md`
- `docs/CHANGELOG.md`

Explanation:

Pinned pgx v5.10.0 and added the narrow boundary that parses the redacted
`DATABASE_URL` into a pgx pool configuration. The application overrides every
connection-string pool-size and lifetime control used by alpha with fixed,
bounded values and caps connection establishment at five seconds. A missing
secret fails before pgx can fall back to ambient `PG*` process variables, and
parse failures return a fixed diagnostic that cannot echo the connection
string.

Verification:

- Red-before-green tests for missing-secret rejection and hostile
  connection-string pool overrides
- pgx v5.10.0 `pgxpool.Config`, `ParseConfig`, and `NewWithConfig` contracts and
  source inspected
- `go test -mod=readonly -race -cover ./internal/config` with 100% statement
  coverage
- `make verify`

Risks / non-goals:

- This unit constructs configuration only; it does not open a connection,
  create a pool, check schema compatibility, or make readiness succeed.
- Pool lifecycle and PostgreSQL integration evidence remain the next A1-02
  units.

### 2026-09-01 05:20 CDT — Wire the bounded HTTP executable lifecycle

Commit: `b4e0a7ae9b48b4fbf4c175bfa127a18cea5731c3`

Affected files:

- `cmd/forum/main.go`
- `cmd/forum/main_test.go`
- `internal/app/http_handler.go`
- `internal/app/http_handler_test.go`
- `internal/app/http_server.go`
- `internal/app/http_server_test.go`
- `internal/app/http_lifecycle.go`
- `internal/app/http_lifecycle_test.go`
- `docs/implementation-spec.md`
- `docs/CHANGELOG.md`

Explanation:

Added the forum executable, immutable configuration wiring, JSON logging,
cryptographic request-ID middleware composition, explicit HTTP transport
limits, numeric listener binding, signal cancellation, bounded graceful drain,
and forced-close failure reporting. Readiness remains deliberately fail-closed
until A1-02 installs the PostgreSQL compatibility check. Cancellation observed
before listener ownership transfer aborts startup; cancellation after transfer
uses bounded shutdown. The first termination signal is unregistered before it
cancels the service context, restoring default handling for a second signal.

Verification:

- Red-before-green tests for handler composition, server controls, and every
  lifecycle failure branch
- `internal/app` race tests with 100% statement coverage
- Configuration-error secrecy and executable start/stop/listen-failure tests
- `make verify`

Risks / non-goals:

- The unavoidably process-terminating `main` wrapper is not invoked in-process
  by unit tests; tested service behavior lives in `run` and `internal/app`.
- Startup truth is established by readiness and deployment probes; the process
  does not emit a potentially blocking pre-serve "started" event.
- PostgreSQL, migrations, assets, sessions, OIDC, and forum routes remain later
  alpha units.

### 2026-09-01 04:00 CDT — Add bounded HTTP observability

Commit: `bf1131eab34cc0dacb2a0f769b63114baedb3f89`

Affected files:

- `internal/observability/context.go`
- `internal/observability/context_test.go`
- `internal/observability/request_id.go`
- `internal/observability/request_id_test.go`
- `internal/observability/request_id_middleware.go`
- `internal/observability/request_id_middleware_test.go`
- `internal/observability/response_observer.go`
- `internal/observability/response_observer_test.go`
- `internal/observability/recovery_middleware.go`
- `internal/observability/recovery_middleware_test.go`
- `internal/observability/access_log_middleware.go`
- `internal/observability/access_log_middleware_test.go`
- `docs/architecture.md`
- `docs/implementation-spec.md`
- `docs/CHANGELOG.md`

Explanation:

Added server-generated 128-bit request IDs, request context propagation,
bounded structured completion logs, response-state observation, and sanitized
panic handling. Inbound identifiers and recovered panic values are never
trusted or logged. A panic after response commitment closes through net/http's
documented quiet-abort boundary rather than appending a false error response.
An uncommitted panic discards application-added headers before returning its
bounded error response. A downstream quiet-abort sentinel is propagated
unchanged and logged once as an aborted request without inventing a status.

Verification:

- Focused red-before-green tests for every production function and method
- Attacker-controlled ID, invalid generator, query secrecy, panic secrecy,
  informational-then-final status handling, stale-header removal,
  invalid-status recovery, committed-response, quiet-abort propagation, and
  missing-dependency cases
- `make verify`
- Package statement coverage report

Risks / non-goals:

- Middleware composition into the executable is the next unit.
- Streaming, connection hijacking, authenticated-user attribution, and general
  error classification are outside this foundation slice.

### 2026-09-01 03:45 CDT — Assemble immutable startup configuration

Commit: `9ac37c6928bf095f122dc97b69bdf17e04c9dafa`

Affected files:

- `internal/config/config.go`
- `internal/config/config_test.go`
- `internal/config/secret.go`
- `internal/config/secret_test.go`
- `docs/implementation-spec.md`
- `docs/CHANGELOG.md`

Explanation:

Added the all-or-nothing startup loader and a formatting-safe secret value.
Every required key is read once. Non-database values pass bounded parsers, and
the result is admitted only when production transport, session lifetime, and
confidential-client invariants all hold. The database string is treated as an
opaque nonempty secret until pgx validates it in A1-02. Failed loads return a
zero configuration.

Verification:

- Focused red-before-green tests for secret redaction, template isolation, and
  loading
- Missing, malformed, default, production, and cross-field failure matrices
- `make verify`
- Package statement coverage report

Risks / non-goals:

- Environment acquisition is dependency-injected; executable `os.LookupEnv`
  wiring remains separate.
- PostgreSQL syntax and connection validation remain an explicit A1-02 startup
  gate; this loader checks only that `DATABASE_URL` is present and nonempty.
- Secret-bearing fields and the value type are unexported. Future database and
  OIDC client wiring must not add a general-purpose public reveal method.

### 2026-09-01 03:30 CDT — Close logging and cookie-name configuration

Commit: `4d9703d0ac21c9f97e006a12676d4d45c323c9f4`

Affected files:

- `internal/config/log_level.go`
- `internal/config/log_level_test.go`
- `internal/config/session_cookie_name.go`
- `internal/config/session_cookie_name_test.go`
- `docs/implementation-spec.md`
- `docs/CHANGELOG.md`

Explanation:

Added a closed structured-log threshold with an `info` default and an
RFC-cookie-token session name with an application-specific default. Cookie
prefixes with incompatible browser-enforced path or transport semantics are
rejected rather than silently producing unusable sessions.

Verification:

- Focused red-before-green unit tests for both production units
- Go `net/http.Cookie.Valid` contract and source checked
- `make verify`
- Package statement coverage report

Risks / non-goals:

- Cookie attributes are set by the later session HTTP boundary, not this
  parser.
- Logger construction and secret-redaction handlers remain separate work.

### 2026-09-01 03:20 CDT — Validate the exact Authentik issuer

Commit: `4d6fedd85768271a7acc91cc5529d5676cea0f0c`

Affected files:

- `internal/config/oidc_issuer.go`
- `internal/config/oidc_issuer_test.go`
- `docs/implementation-spec.md`
- `docs/CHANGELOG.md`

Explanation:

Added exact OIDC issuer validation using Authentik's recommended per-provider
issuer form. Production requires HTTPS; Authentik global issuer mode,
credentials, queries, fragments, relative URLs, and inputs that would be
normalized are rejected. Diagnostics do not echo malformed issuer values.

Verification:

- Authentik issuer-mode and discovery endpoint documentation checked
- Focused red-before-green unit tests
- `make verify`
- Package statement coverage report

Risks / non-goals:

- Discovery retrieval and metadata comparison are not implemented in this
  unit.
- Authentik global issuer mode remains unsupported until a separate trusted
  provider-specific discovery location is modeled.
- Authentik remains login-only; issuer claims do not grant local authority.

### 2026-09-01 03:15 CDT — Add immutable runtime primitives

Commit: `35297caacc9facbc5b3cc58cfbd61dab0c1ab774`

Affected files:

- `internal/config/environment.go`
- `internal/config/environment_test.go`
- `internal/config/duration.go`
- `internal/config/duration_test.go`
- `internal/config/listen_addr.go`
- `internal/config/listen_addr_test.go`
- `docs/implementation-spec.md`
- `docs/CHANGELOG.md`

Explanation:

Added exact environment selection, positive duration parsing, and numeric
listen-address validation as the next immutable configuration units.
Production binds only to IPv4/IPv6 loopback, preserving Caddy as the public
edge. Malformed raw values are not echoed into diagnostics.

Verification:

- Focused red-before-green unit tests for each production unit
- `make verify`
- Package statement coverage report

Risks / non-goals:

- The aggregate environment loader and executable service lifecycle remain
  incomplete.
- Development/test may bind non-loopback deliberately; production may not.

### 2026-09-01 03:00 CDT — Make the governance lock structural

Commit: `1401728efb30c6fa942f4bd4542f43fd45c96056`

Affected files:

- `docs/implementation-spec.md`
- `docs/verification.md`
- `docs/release-operations.md`
- `docs/CHANGELOG.md`

Explanation:

The governance lock is now a real schema invariant: one boolean primary key
constrained to true, seeded by migration, protected from runtime mutation, and
validated by readiness. This prevents missing or multiple coordination rows
from defeating administrator-continuity serialization.

Verification:

- Schema/cardinality contract review
- Readiness and runtime-privilege failure cases added to verification
- Markdown link and whitespace checks

Risks / non-goals:

- Migration privileges can still repair a damaged row deliberately; runtime
  application privileges cannot.

### 2026-09-01 02:50 CDT — Serialize administrator invariants

Commit: `652b48961dcb5b41464f8676f4bf5902e5b1416d`

Affected files:

- `docs/architecture.md`
- `docs/implementation-spec.md`
- `docs/verification.md`
- `docs/CHANGELOG.md`

Explanation:

Administrator continuity now has a concrete PostgreSQL mechanism: bootstrap
and administrator role/suspension transitions lock one seeded governance row
before checking the active-administrator invariant. “Active” means an
administrator role with no effective suspension. No cached count can drift.

Verification:

- Transaction and concurrency contract consistency review
- Completeness oracle added to the verification plan
- Markdown link and whitespace checks

Risks / non-goals:

- This is coordination state, not a generic distributed lock framework.
- The schema and transaction code remain implementation work in A1-02/A1-08.

### 2026-09-01 02:40 CDT — Preserve administrator continuity

Commit: `ffa8dfacb75a37185e02f521a587ddd24fcc818f`

Affected files:

- `docs/prd.md`
- `docs/architecture.md`
- `docs/implementation-spec.md`
- `docs/verification.md`
- `docs/CHANGELOG.md`

Explanation:

The operator bootstrap grant is admitted only while no active administrator
exists and is serialized against role changes. Local demotion and suspension
transitions must reject any result that leaves zero active administrators.
This closes both persistent operator bypass and accidental governance lockout.

Verification:

- Cross-document role/bootstrap consistency review
- Concurrency and failure-path requirements added to the verification contract
- Markdown link and whitespace checks

Risks / non-goals:

- The mechanism remains unimplemented in this documentation-only correction.
- Emergency recovery after external database damage is an operator incident
  procedure, not a hidden bypass in the normal bootstrap command.

### 2026-09-01 02:25 CDT — Make Authentik authentication-only

Commit: `1de1ac2cfbd99241780ad086023f4d6d032b55b1`

Affected files:

- `docs/prd.md`
- `docs/architecture.md`
- `docs/implementation-spec.md`
- `docs/feature-plan.md`
- `docs/verification.md`
- `docs/release-operations.md`
- `docs/CHANGELOG.md`

Explanation:

Authentik now proves identity only. GOTTH Board owns roles, local groups,
restricted-area membership, suspensions, and moderation. OIDC claims cannot
grant local authority. The first administrator is created through an explicit
audited operator command against an existing issuer/subject identity. This
upstream correction prevents the implementation from building the rejected
Authentik-group authorization model.

Verification:

- Canonical-document terminology scan for removed Authentik-group authority
- Requirement and acceptance-boundary consistency review
- Markdown link and whitespace checks

Risks / non-goals:

- This change does not implement the OIDC or local authorization mechanisms.
- Exact Authentik issuer/client, first administrator subject, and deployment
  values remain runtime owner inputs.

## 2026-09-01 — Alpha foundation begins

- Implementation commit: `8382c8f374ec22d60a90786bb0006e4a06776631`
- Evidence commit: `ac0df0a4d0c2f7966bbad02290262ce038aa0830`
- Review-fix commits: `cf2bebd5c25b67b3ca7e5939a2472218fe9c5e22`,
  `d5e9f5e3e947ed40abc40ab99704a3b9a0484a77`,
  `3c1c784a8ef6c9d13630ba31422e0712c35b75da`
- Branch: `feature/alpha-1-foundation`
- Requirements partially addressed: READ-005, OPS-002, OPS-005
- Delivery item: A1-01, incomplete

Changed:

- Pinned Go 1.26.6 in `go.mod` and `.go-version`.
- Added reproducible `make build`, `make test`, and `make verify` commands.
- Added exact base-path validation and public-base-URL validation, including
  production HTTPS enforcement and rejection of traversal, encoded separators,
  credentials, queries, fragments, malformed escaping, and empty hostnames.
  Malformed URL diagnostics redact the raw configured value.
- Added a browser URL builder that preserves the configured prefix, escapes
  each path segment, and rejects empty or dot-segment ambiguity.
- Added a standard-library HTTP shell with liveness and a deliberately failing
  readiness endpoint until PostgreSQL/schema checks exist.

Verification:

```text
git diff --check
make build
make verify
go test -mod=readonly -coverprofile=/tmp/gotth-bb-all.cover ./...
go tool cover -func=/tmp/gotth-bb-all.cover
```

All commands passed on linux/amd64 with `go1.26.6-X:nodwarf5`. The touched
production functions have 100% statement coverage, including failure paths.
The race detector passed. No external dependencies were selected or fetched.

Performance evidence is not applicable to this bounded startup/health slice:
it makes no throughput or latency claim, and all health output is constant
size. The URL/config parsers are linear in their bounded configuration input.

Still incomplete in A1-01: the full immutable configuration loader, structured
logging, request IDs, panic boundary, PostgreSQL-backed readiness, executable
service lifecycle, Templ/Tailwind asset build, CI, and secret scanning.

Before admission, rollback is deletion of the isolated feature branch and
worktree. After admission by a merge commit, revert that merge commit; do not
rewrite shared history. If these commits are admitted individually, revert
them newest-to-oldest using the exact hashes recorded above.
