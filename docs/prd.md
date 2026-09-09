# Product requirements document

## Document control

| Field | Value |
| --- | --- |
| Product | GOTTH Board |
| Status | Draft for owner review |
| Document version | 0.6 |
| Initial development URL | `https://bb.alhstudios.com/` |
| First delivery target | `1.0.0-alpha.1` |
| First stable target | `1.0.0` |
| Identity authority | Authentik |

## 1. Problem

GOTTH Board is a self-hosted bulletin board where communities can hold durable,
searchable discussions without creating another password database. The board
must support open areas, member-only areas, read-only areas, and areas
restricted by forum-local group membership. Authorization must apply to every
way content can be discovered, not merely to the final page handler.

Existing generic forum products either own identity themselves, hide access
control behind sprawling plugin systems, or are awkward to operate beneath a
path prefix. This product will provide the required forum mechanism directly:
Go on the server, PostgreSQL as durable state, server-rendered Templ views,
HTMX for targeted interaction, Tailwind CSS for presentation, and Authentik
for identity.

## 2. Product goals

- Provide a useful discussion board in `1.0.0`, not a disposable prototype.
- Delegate authentication and primary identity lifecycle to Authentik.
- Keep forum-specific roles, groups, profiles, content, suspensions, and
  moderation history inside the forum.
- Make access rules explicit, auditable, and impossible to bypass through
  search, feeds, counts, direct URLs, or partial HTMX requests.
- Preserve simple page navigation and stable links beneath the configured
  public base URL.
- Ship each major version as an independently operable release.
- Keep deployment, backup, rollback, and failure behavior documented.

## 3. Non-goals

- Local passwords, password recovery, or independent multifactor auth.
- A real-time chat replacement in version 1.0.
- Topic-level access-control exceptions in version 1.0.
- Arbitrary user-supplied HTML.
- Simultaneous support for Markdown, BBCode, and multiple editor models.
- Multi-tenant hosting in version 1.0.
- SCIM provisioning before version 4.0.
- Native mobile applications.

## 4. Users and roles

### 4.1 Visitor

An unauthenticated person. A visitor can discover and read only areas whose
visibility is public. A visitor cannot create content or submit reports.

### 4.2 Member

An Authentik user with a successfully verified OIDC identity and a local forum
account. A member can read authenticated areas and create content where the
area's posting mode, local group rules, and local account state permit it.

### 4.3 Moderator

A member granted the forum-local moderator role by an administrator. A version
1.0 moderator has global content-moderation access, including restricted
areas. Every role grant and content-changing moderator action is audited.

### 4.4 Administrator

A member granted the forum-local administrator role through the exact-subject
first-run procedure, the operator fallback, or by an existing administrator.
An administrator manages areas, access rules, local roles and groups, local
account state, and site settings. Administrative changes are audited.

### 4.5 Identity authority

Authentik determines whether authentication succeeds and supplies the stable
OIDC subject and approved display claims. The forum owns participation, roles,
groups, restricted-area membership, and local suspension state. Authentik
claims never grant moderator, administrator, or area-access privileges.

## 5. Product rules

### 5.1 Identity and sessions

- **ID-001:** The forum shall authenticate exclusively through Authentik using
  OIDC Authorization Code flow with PKCE, state, and nonce validation.
- **ID-002:** The durable external identity key shall be the pair of verified
  issuer and subject. Email shall never be an identity key.
- **ID-003:** The first successful login shall create the local forum account
  just in time.
- **ID-004:** Later logins shall refresh only explicitly approved profile
  fields from verified claims and shall not overwrite forum-local roles or
  groups.
- **ID-005:** The forum shall own roles, groups, group membership, and
  area-access rules. Every role or group-membership mutation shall be explicit,
  authorized, and audited. No transition may leave the forum without an active
  administrator.
- **ID-006:** The forum shall maintain server-side sessions with rotation,
  expiration, revocation, and a cookie scoped to the configured base path.
- **ID-007:** A local suspension shall deny participation even while Authentik
  authentication remains valid.
- **ID-008:** Disabling an identity in Authentik shall take effect no later than
  the configured maximum session-validation interval.
- **ID-009:** The forum shall not store passwords, recovery codes, or Authentik
  administrative credentials.
- **ID-010:** A fresh deployment may expose one first-run administrator claim
  only to the freshly reauthenticated local account whose verified issuer and
  subject match the immutable deployment configuration. The claim shall be
  CSRF-protected, serialized with administrator governance, audited, revoke the
  elevating session, and close permanently after the first successful
  bootstrap. The first arbitrary registrant shall never become administrator.
- **ID-011:** The forum shall delegate self-registration to one configured
  Authentik enrollment flow. Successful enrollment shall create an Authentik
  identity eligible only for the board application and shall still create only
  an ordinary local `member` on first OIDC login.
- **ID-012:** A deployment shall not advertise registration as board-only while
  another application in the shared Authentik directory remains accessible to
  all users. Every other application must explicitly exclude the board
  enrollment group, or the board must use a separately isolated identity
  authority.
- **ID-013:** Registration and first-run administration shall be independent.
  Enabling registration shall not grant, imply, or restore any forum-local
  role, group membership, area permission, or administrative authority.

### 5.2 Area access model

Visibility and posting behavior are separate axes. Treating read-only as a
visibility value would prevent a public read-only announcement area or a
group-restricted read-only archive.

Visibility values:

- `public`: visible to everyone.
- `authenticated`: private/member-only visibility; visible only to accepted
  forum members.
- `groups`: visible only to members whose current forum-local group set
  intersects the area's configured groups.

Posting modes:

- `normal`: eligible members may create topics and replies.
- `read_only`: only moderators and administrators may publish.
- `archived`: no new publishing; an administrator may restore the area.

Requirements:

- **ACL-001:** Every area shall have exactly one visibility value and one
  posting mode.
- **ACL-002:** Area rules shall be inherited by every contained topic and post.
- **ACL-003:** Version 1.0 shall not implement topic-level visibility
  exceptions.
- **ACL-004:** Authorization shall be enforced server-side for full-page,
  HTMX, form, search, count, feed, report, and administrative requests.
- **ACL-005:** Unauthorized content shall not appear in recent activity,
  unread state, profiles, search snippets, counts, breadcrumbs, feeds, related
  topics, or direct URL responses.
- **ACL-006:** Unauthorized direct requests shall not reveal whether the hidden
  area, topic, or post exists.
- **ACL-007:** Changes to visibility, posting mode, or permitted groups shall
  create immutable audit events.
- **ACL-008:** Moderators and administrators may access restricted areas for
  their duties; this elevated access shall be explicit and auditable.

### 5.3 Forum structure and content

- **FORUM-001:** Version 1.0 shall support one level of areas/categories.
- **FORUM-002:** An area shall have a name, slug, description, display order,
  visibility, posting mode, and optional group restrictions.
- **FORUM-003:** Members shall create topics in eligible areas and replies to
  visible posts inside eligible topics.
- **FORUM-004:** A topic shall form one rooted reply tree. The first post is the
  root; every later post identifies one immutable parent in the same topic;
  sibling replies are ordered oldest-first by immutable post number.
- **FORUM-005:** Topics may be pinned, locked, moved, hidden, restored, or
  archived by authorized staff.
- **FORUM-006:** Topics and posts shall have stable identifiers and canonical
  URLs beneath the configured public base URL.
- **CONTENT-001:** Authors shall write Markdown and preview the sanitized
  rendered result before publication.
- **CONTENT-002:** Supported version 1.0 formatting shall implement GitHub
  Flavored Markdown: CommonMark plus tables, strikethrough, task lists, and
  GFM autolinks/linkification. It shall also include fenced and inline code.
- **CONTENT-003:** Raw HTML shall be disabled or sanitized to the documented
  allowlist.
- **CONTENT-004:** Authors shall edit their own visible content and see an
  edited timestamp.
- **CONTENT-005:** Author deletion shall be soft deletion governed by forum
  policy; hard purge is an administrative retention action.
- **CONTENT-006:** Validation failure shall preserve submitted content and
  return field-specific errors.
- **CONTENT-007:** Version 1.0 shall not include attachments, polls, arbitrary
  embeds, or private messaging.

### 5.4 Reading and discovery

- **READ-001:** The forum shall provide an area index, topic lists, threaded
  topic pages, recent activity, and bounded pagination or continuation.
- **READ-002:** Signed-in members shall have new/unread indicators and a jump
  to first unread action.
- **READ-003:** PostgreSQL full-text search shall support bounded web-search
  text syntax, author ID, area, and inclusive UTC date filters; return typed
  topic/post results with honest bounded result and ranking semantics; and
  provide access-filtered recent post activity with bounded continuation.
- **READ-004:** Search, activity, counts, ranks, snippets, continuation, and
  direct result targets shall apply the same area/topic access predicate as
  direct reads before restricted rows contribute fields or terminality.
- **READ-005:** Pages shall provide breadcrumbs and canonical URLs that include
  the configured external base path, including a root deployment.
- **READ-006:** Threaded topic reads shall preserve deterministic parent/child
  order and reply-to context without requiring an unbounded topic response.

### 5.5 Moderation

- **MOD-001:** Members shall report a topic, post, or user with a reason.
- **MOD-002:** Moderators shall process reports through a moderation queue.
- **MOD-003:** Moderators shall hide, restore, redact, lock, unlock, pin, unpin,
  and move content as applicable.
- **MOD-004:** Moderators shall warn, mute, suspend, and reinstate local forum
  accounts.
- **MOD-005:** The forum shall apply configurable request and publishing rate
  limits, including stricter limits for new accounts.
- **MOD-006:** Version 1.0 shall support basic blocked-link and blocked-domain
  rules.
- **MOD-007:** Moderation and administrative mutations shall append an audit
  event containing actor, target, action, reason, and timestamp.
- **MOD-008:** Audit events shall not be editable through application features.

### 5.6 Administration

- **ADMIN-001:** Administrators shall create, rename, reorder, archive, restore,
  and configure areas.
- **ADMIN-002:** Administrators shall view local forum accounts and manage
  forum-local suspension state.
- **ADMIN-003:** Administrators shall manage forum-local roles, groups, group
  membership, and area group restrictions without code changes. The bootstrap
  operator grant shall be unavailable once an active administrator exists.
- **ADMIN-004:** Administrators shall configure site name, description, basic
  branding, and community rules.
- **ADMIN-005:** Version 1.0 shall provide basic membership, activity, and
  moderation counts subject to authorization.

### 5.7 Experience and accessibility

- **UX-001:** The interface shall work at common mobile and desktop widths.
- **UX-002:** Core flows shall be usable with keyboard navigation and visible
  focus.
- **UX-003:** Forms shall have labels, error association, and usable status
  announcements.
- **UX-004:** HTMX shall enhance targeted interactions without making access
  rules or validation depend on client-side code.
- **UX-005:** A failed HTMX request shall remain diagnosable and shall not leave
  the page displaying a success state.
- **UX-006:** Every complete page footer shall attribute the immutable product
  as `Powered by GOTTH Board` linking to `https://github.com/gotthboard`.
  Configurable site name, description, theme, base URL, or home URL shall not
  alter that product name or target. HTMX fragments remain footer-free.
- **UX-007:** Area administration create, edit, and group-access controls shall
  remain visibly distinct, labeled, keyboard-focusable, and usable without
  horizontal overflow from 320 CSS pixels through desktop widths. Text fields,
  text areas, and selects shall not disappear into the page background; mobile
  controls stack before bounded multi-column layouts are introduced.

### 5.8 Security and operation

- **SEC-001:** State-changing requests shall require CSRF protection.
- **SEC-002:** Cookies shall be Secure, HttpOnly, SameSite-protected, and scoped
  as narrowly as the flow permits.
- **SEC-003:** Responses shall use a documented content-security policy and
  defensive browser headers.
- **SEC-004:** User content shall be escaped by default and sanitized at the
  rendering boundary.
- **SEC-005:** Logs shall exclude session tokens, OIDC tokens, secrets, and
  full sensitive request bodies.
- **OPS-001:** Schema changes shall use ordered PostgreSQL migrations.
- **OPS-002:** The service shall expose liveness and readiness checks without
  exposing private application state.
- **OPS-003:** Logs shall be structured and include request correlation.
- **OPS-004:** Deployment, migration, backup, restore, and rollback procedures
  shall be documented and tested before `1.0.0`.
- **OPS-005:** The service shall fail closed when identity, session, or access
  state cannot be validated.
- **OPS-006:** A version 1.0 release shall include one deployable, pinned
  Compose stack that owns the Board Caddy edge, Authentik server and worker,
  a dedicated Authentik PostgreSQL database, the Board service, and a
  dedicated Board PostgreSQL database. It shall not require an operator's
  pre-existing Caddy, shared identity tenant, or shared application database.
  Only Caddy may accept public traffic; both databases and the application and
  identity upstreams remain non-public. Fresh install, stopped upgrade,
  identity cutover, backup/restore of both durable stores, restart, and
  rollback shall be rehearsed before the stack is offered to Beta testers.

## 6. Version plan

### 6.1 Version 1.0: secure core forum

Version 1.0 contains every requirement in sections 5.1 through 5.8. Its
prerelease sequence is:

- `1.0.0-alpha.1`: first deployable vertical slice.
- `1.0.0-alpha.N`: incomplete but integrated internal test builds.
- `1.0.0-beta.1`: complete version 1.0 feature surface opened for user testing.
- `1.0.0-rc.1`: no known feature gaps; release and rollback rehearsal begins.
- `1.0.0`: stable release after acceptance evidence is complete.

### 6.2 Version 2.0: content and engagement

Subscriptions, watches, bookmarks, mentions, in-app/email notifications,
digests, tags, prefixes, templates, polls, question/answer topics, accepted
answers, reactions, voting, autosaved drafts, scheduled publishing, revision
history, attachments, safe media handling, link previews, saved searches,
related topics, RSS/Atom, and global announcements.

### 6.3 Version 3.0: community and communication

Private and group conversations, abuse reporting for messages, follow/block/
mute/ignore controls, expanded profiles, signatures, member directory, online
presence, reputation, trust levels, ranks, badges, leaderboards, activity feeds,
events, calendars, birthdays, social previews, profile privacy, contact lists,
and browser push.

### 6.4 Version 4.0: governance and enterprise operation

Authentik SCIM, reconciliation, provisioning audit, advanced approval queues,
automated trust restrictions, richer anti-spam controls, filters, duplicate
detection, bulk moderation, escalation and appeals, retention, legal holds,
advanced role management, session/device management, data portability,
importers, analytics, localization, RTL, themes, policy pages, API, webhooks,
and external service integrations.

### 6.5 Version 5.0: ecosystem and scale

ActivityPub federation and federation moderation, real-time updates, advanced
search, recommendations, an extension surface, optional multiple branded
communities, PWA/offline behavior, large-media delivery, high-volume job
isolation, horizontal scaling, read replicas, archival storage, and mature
migration/export compatibility guarantees.

## 7. Alpha.1 acceptance boundary

`1.0.0-alpha.1` is acceptable when all of the following work in one deployed
environment:

1. An eligible Authentik user can sign in and receive a local account.
2. Visitor, member, group member, moderator, and administrator permissions are
   distinguishable.
3. Public, authenticated, and group-restricted visibility is enforced.
4. Normal, read-only, and archived posting modes are enforced.
5. A member can create and read a topic, reply, edit, and soft-delete.
6. A moderator can lock, hide, restore, and suspend through minimal controls.
7. Restricted content does not leak through the available lists or direct
   URLs.
8. PostgreSQL migrations create a fresh database successfully.
9. The interface works beneath the initial development URL
   `https://bb.alhstudios.com/` with correct links, forms, HTMX requests,
   assets, cookies, and OIDC callback.
10. Authentication and access-control tests pass in CI or an equivalent
    reproducible command.

Search, unread state, the complete report queue, polished administration,
advanced spam controls, and final backup/restore evidence may remain incomplete
until beta.

## 8. Alpha.2 acceptance boundary

`1.0.0-alpha.2` establishes the compact, information-dense forum surface used
by the remaining version 1.0 work. It is acceptable when all of the following
work in one deployed environment:

1. The board index presents each visible area with its name, description,
   visible topic count, visible post count, and latest visible post summary.
2. Area statistics and latest-post selection use the same actor-authorized
   visibility predicate as the corresponding area/topic reads; hidden or
   restricted content does not affect an unauthorized actor's result.
3. Topic lists present state, title, starter, reply count, and last activity in
   a compact conventional forum layout without changing canonical URLs.
4. Topic pages present each post as one semantic article with an author panel,
   post metadata, content, permalink, authorized controls, and an explicit
   reply-to relationship.
5. A member can reply to any visible post in an eligible topic. The committed
   reply appears beneath its parent without a full-document refresh; sibling
   order is deterministic and the submitted draft survives validation or
   conflict responses.
6. Nested replies use visible hierarchy and connector cues. Mobile layout caps
   visual indentation without changing logical ancestry, order, canonical
   post URLs, or keyboard traversal.
7. Soft-deleting a post with visible descendants leaves a content-free
   tombstone in the tree so descendants retain their conversational context.
8. Topic reads remain bounded. Continuation preserves tree order and identifies
   an omitted parent through safe metadata or a canonical permalink rather
   than silently presenting a child as a top-level reply.
9. The masthead, breadcrumbs, pagination, notices, forms, and action controls
   use one coherent GOTTH Board visual system inspired by conventional bulletin
   boards without copying phpBB code, markup, images, icons, or branding.
10. The index, topic list, topic page, and publishing controls remain usable at
   320 CSS pixels and at desktop widths without document-level horizontal
   scrolling.
11. In-session interactions retain the alpha.1 HTMX contract: only the
   authoritative board region changes, browser history remains correct, and
   ordinary HTML navigation/form fallbacks remain equivalent.
12. Keyboard focus, semantic table/list relationships, headings, form labels,
   and status announcements remain usable after both full-page and HTMX
   navigation.
13. Focused migration, SQL, store, publishing, handler, rendering,
    access-leakage, responsive, pagination/continuation, and HTMX tests pass
    reproducibly.

Search, unread state, reports, new moderation transitions, group
administration, site-setting administration, and public registration are not
part of this surface-and-threading increment.

## 9. Alpha.3 acceptance boundary

`1.0.0-alpha.3` completes the version 1.0 Markdown authoring and persisted
renderer boundary. It is acceptable when all of the following hold:

1. Create, reply, edit, preview, and publish paths use one deterministic
   Goldmark v1.8.5 GFM renderer followed by the exact Bluemonday v1.0.27 forum
   sanitizer. The migration uses that same p2 path, with one explicit
   compatibility exception for exact admitted p1 HTML that would exceed the
   unchanged persistence bound under p2.
2. Raw HTML stays disabled. Only the elements and attributes emitted by the
   enabled CommonMark/GFM features survive sanitization; style, event-handler,
   arbitrary form, and unsafe URL content remains forbidden.
3. Tables, strikethrough, task lists, and GFM autolinks work, while task-list
   checkboxes remain disabled and cannot become interactive controls.
4. The persisted renderer version changes immutably. A population-linear,
   restart-safe migration uses bounded read-only and mutation transactions to
   rebuild old derived HTML from canonical Markdown without overwriting a
   concurrent edit. The mutation pass persists a nullable post-ID keyset cursor
   atomically with each batch, so retry and restart do not rescan an already
   converted prefix. When exact application-valid
   p1 HTML cannot fit after p2 expansion, it is preserved byte-for-byte under a
   distinct compatibility marker rather than stranding the release. Readiness
   fails closed until every ordinary post is current p2 or explicitly
   p1-preserved and the exact writer constraints are validated. The final
   validation performs a whole-table scan under PostgreSQL's documented table
   lock; maintenance evidence must include that phase separately.
5. The native authoring toolbar provides bold, italic, link, block quote,
   inline code, fenced code, ordered-list, unordered-list, table, task-list,
   and strikethrough shortcuts with selection, caret, multiline, and
   applicable toggle behavior. Image authoring remains excluded with
   attachments and arbitrary embeds; it is deferred to version 2 and
   `gotth-media`, where upload, scanning, privacy, quota, retention, and
   failure behavior can be defined together.
6. Toolbar buttons use native semantics, accessible names and tooltips, visible
   focus, and document-order keyboard navigation. Plain Markdown submission
   remains fully usable with JavaScript absent or failed. The toolbar is named
   by the full SHA-256 of its exact bytes so the immutable cache contract is
   safe across upgrades and rollbacks; the former mutable `v1` asset URL is
   not served.
7. Normative GFM, sanitizer/XSS, dangerous-link, deterministic-rendering,
   boundary-size, migration restart/concurrency, server integration, HTMX,
   and toolbar behavior tests pass reproducibly.

Mermaid, math, footnotes, emoji shortcodes, mentions, issue references, syntax
highlighting, rich-text editors, and WYSIWYG behavior are not part of alpha.3.

## 10. AN-02 acceptance boundary

AN-02 admits search and recent activity under these visible constraints:

1. `GET /search` accepts `q`, author ID, area slug, inclusive UTC `from`/`to`
   dates, and page 1 or 2. At least `q` or author is required. Text uses
   PostgreSQL web-search terms, quoted phrases, `OR`, and unary `-`; prefix and
   fuzzy matching are not promised.
2. Search considers at most the newest 50 authorized matches, presents 25 per
   page, and labels only whether a 51st authorized match exists. Text relevance
   is approximate and orders only those 50; it is not a global best-rank claim.
   Page 2 is a new database snapshot and may duplicate or omit rows changed
   concurrently.
3. Topic titles and undeleted, unredacted post bodies are separate result
   types. A matching topic title suppresses only its matching root-post body.
   Post excerpts are the first at most 300 runes of escaped, unhighlighted
   normalized visible text.
4. `GET /activity` returns undeleted, unredacted posts newest first, 25 per
   page. Its authenticated cursor is valid for at most 24 hours and is bound to
   the visitor or the authenticated user's current role and complete local
   group set. Authorization precedes the 26-row continuation fence; restricted
   rows cannot suppress older public activity or reveal restricted occupancy.
5. `GET /posts/{postID}` is a bounded direct result target. It performs a
   primary-key-started authorized read and never enumerates a topic tree to
   derive a page number.
6. All three routes and their errors work as ordinary HTML and equivalent HTMX
   responses, remain base-path aware, and are `private, no-store`. Search and
   Recent activity are ordinary navigation links; JavaScript is optional.
7. Common or hostile discovery queries may fail with a fixed `503` at their
   bounded deadline. They never return partial or less-authorized results to
   manufacture success. Saturation affects only search/activity, not publishing
   or unrelated reads.
8. AN-02 does not impose publication quotas, change `restart: unless-stopped`,
   replace the service supervisor, add an external search service, or invent a
   new backup/deployment authority.

## 11. AN-03 acceptance boundary

AN-03 admits signed-in unread state without turning safe reads into hidden
mutations:

1. Every visible topic is in exactly one member-only state: `new` when no read
   marker exists and at least one currently readable post by another author
   exists, `unread` when a marker exists below such a post, or `read` otherwise.
   Visitors receive no read-state field, count, or control.
2. A read marker is a monotonic per-user/per-topic acknowledgment through an
   immutable `post_number`. Ordinary topic, post, search, and activity `GET`s
   never change it. `POST /topics/{topicID}/read`, protected by the existing
   session, revalidation, and CSRF boundary, marks through the highest currently
   readable post by another author selected by the server. It accepts no
   client-owned watermark. This POST is the only read-workflow marker mutation;
   existing hard-delete foreign-key cascades still remove marker rows.
3. A member's own posts never create that member's `new` or `unread` state.
   Topic/reply publication, edits, moderation, direct-post reads, search,
   activity, and first-unread navigation do not change markers.
4. The board index shows one exact authorized unread-topic count per visible
   area, combining `new` and `unread`. The bounded area topic page shows the
   distinct per-topic state and a first-unread action. No global unread feed,
   notification system, or approximate count is introduced.
5. `GET /topics/{topicID}/unread` is read-only. It selects the lowest currently
   readable other-authored `post_number` above the marker and redirects to that
   post on its canonical bounded tree page. If the tree ordinal exceeds the
   existing 10,000-page topic limit, it falls back to the bounded direct-post
   URL. With no unread post it redirects to the canonical topic root.
6. The same area/topic predicate used by direct reads runs before any topic
   identity, state, count, target, page ordinal, or terminal decision can
   contribute. Missing, deleted, hidden, and inaccessible topics remain
   indistinguishable at the route boundary.
7. Deleted, redacted, or current-actor-authored posts never create unread state.
   A later restore of another author's post may become unread only when its
   immutable number is above the stored marker; restoring an older acknowledged
   position does not move the marker backward.
   Topic hard deletion cascades markers; access revocation merely hides them.
8. Authenticated full-page and HTMX behavior remain base-path-safe,
   `private, no-store`, bounded, and usable without JavaScript; visitor pages
   retain their existing non-personalized cache behavior. AN-03 adds no tracking
   timestamp visible to other users, external service, background worker,
   cursor key, publication quota, or deployment authority.

## 12. AN-04 acceptance boundary

AN-04 completes version 1.0 administration without turning the board into an
identity provider or a general analytics system:

1. Administrators retain one audited area surface for create, rename,
   deterministic `(display_order, id)` ordering, visibility/group restrictions,
   and posting mode. `archived` remains readable to an otherwise authorized
   actor but rejects new topics and replies; restore explicitly selects
   `normal` or `read_only`. Published slugs remain immutable and areas are not
   deleted in version 1.0.
2. Administrators can page through local accounts, inspect one account's local
   role and suspension state, page through its local group assignments, and use
   the existing audited suspension/reinstatement boundary. No email, avatar URL, OIDC
   subject, token, session identifier, IP, or user-agent data appears in the
   account administration surface.
3. Administrators can create and rename forum-local groups and grant or revoke
   one account/group membership per audited request. Group lists and assignment
   state are paged rather than constrained by a made-up installation-wide
   quota. Version 1.0 does not delete groups;
   this prevents an apparently simple administrative action from silently
   cascading area-access changes. Area restrictions continue to reference the
   same local groups and take effect on the next protected request.
4. Administrators can change another active local account among the closed
   `member`, `moderator`, and `administrator` roles. Self-role changes are
   rejected. Every role change serializes with first-administrator governance,
   preserves at least one active administrator, revokes the target's sessions,
   and records one immutable audit event before commit. OIDC claims never grant
   or restore a local role or group.
5. Administrators can update one singleton site name, short description,
   closed built-in brand theme, and community-rules Markdown. The admitted GFM
   renderer and sanitizer produce the stored rules HTML; arbitrary CSS,
   JavaScript, remote logos, tracking pixels, and embeds are excluded. The
   public `/rules` page and every full document use current database state; no
   process-local cache creates a hidden propagation window.
6. The administrator dashboard exposes exact local account totals and closed
   role/suspension buckets, undeleted topic and undeleted/unredacted post totals,
   and open/in-review report totals. These values are private administrator
   state, never added to visitor/member responses, shared caches, logs, metric
   labels, or unauthenticated terminal behavior.
7. Every administrative read and mutation requires a current unsuspended local
   administrator loaded from PostgreSQL. Mutations use the existing session,
   revalidation, CSRF, request-ID, bounded-form, transaction, audit, and unknown-
   commit rules. Missing, stale, malformed, conflicting, or inaccessible state
   fails closed without partial mutation.
8. Administration remains base-path-safe, `private, no-store`, bounded, and
   usable with ordinary HTML, HTMX, keyboard navigation, and JavaScript absent.
   AN-04 adds no Authentik credentials or management API, account creation,
   impersonation, bulk administration, external analytics, new service,
   background worker, deployment authority, or retention policy.

## 13. AN-05 acceptance boundary

AN-05 adds basic abuse controls without inventing a distributed policy service
for the version 1.0 single-process deployment:

1. Every non-health, non-static HTTP request is charged before authentication,
   body parsing, or PostgreSQL work to one bounded process-local client window.
   The admitted production profile permits 300 requests per 60 seconds and
   retains at most 4,096 client windows. Caddy overwrites, rather than appends,
   the one client-address header delivered over the loopback-only upstream;
   malformed or ambiguous identity fails closed. Client addresses never enter
   application logs, PostgreSQL, retained evidence, or response bodies.
2. The request limiter is deliberately one-instance state. Restarting the Go
   process clears its windows, and multiple application processes would each
   enforce an independent budget. That limitation is admissible only while the
   version 1.0 deployment remains one Caddy route to one Go process. Health and
   content-addressed static assets remain available during limiter saturation.
3. Successful topic and reply creation share one durable per-account,
   account-anchored fixed window. The admitted profile permits 10 publications
   per 10 minutes; accounts younger than 24 hours permit 3 per 10 minutes.
   Every actor, including staff, is subject to the limit. Edits and previews do
   not spend publication capacity because they create no post, but blocked-link
   policy still applies to both.
   New-account strictness applies to this authenticated publication budget,
   not the pre-authentication client-address request budget.
4. Publication accounting is constant-size state on the local account row and
   commits in the same PostgreSQL transaction as the topic or reply. Concurrent
   requests serialize on that account, and validation, authorization, database,
   cancellation, or commit failure cannot consume capacity without the matching
   publication. A restart does not reset this durable window. The fixed-window
   edge can admit up to twice the configured count across two adjacent windows;
   version 1.0 does not pretend this is a rolling-window or token-bucket limit.
5. Operators provide one bounded, read-only startup rules file containing
   canonical blocked domains and canonical exact HTTP(S) URLs. A domain rule
   matches that IDNA-normalized host and its subdomains. An exact URL rule uses
   normalized scheme, host, default port, path, and query while ignoring a
   fragment. The policy checks resolved Markdown links, images, and automatic
   links using the admitted GFM parse; code spans/blocks and ordinary text do
   not become false positives. Local relative links remain outside external-
   link policy, while protocol-relative network paths and destinations with an
   ASCII backslash are rejected so browser URL normalization cannot manufacture
   an external-domain bypass.
6. Topic, reply, edit, their preview paths, and administrator community-rules
   updates use the same immutable link policy and fail before persistence when
   any destination is blocked. Existing
   stored content is not retroactively rewritten or hidden. Version 1.0 does
   not fetch destinations, follow redirects, run reputation services, inspect
   DNS answers, or claim that exact-URL rules defeat arbitrary URL shorteners.
7. Rate rejection returns `429` with an integer `Retry-After`; inability to
   admit a new client window returns bounded `503` with `Retry-After: 1`; a
   blocked destination returns the existing field-safe `422` presentation.
   Full-page and HTMX responses preserve the submitted draft where it has
   already been safely parsed, reveal no key, address, account age, counter, or
   matched rule, and remain useful with JavaScript absent.
8. Operators can count fixed rejection classes by bounded structured events and
   the existing route/status access record. Logs and metric labels contain no
   client address, user identifier, raw URL, query, Markdown, destination,
   rule, or quota key. Configuration is validated once at startup; changing a
   limit or rules file requires an intentional restart.

AN-05 adds no CAPTCHA, reputation network, DNS block service, content scanning
worker, automatic moderation action, account deletion, global IP identity,
horizontal rate-limit coordination, release authority, or deployment.

## 14. Beta.1 acceptance boundary

`1.0.0-beta.1` is the first version 1.0 build offered to designated ordinary
test users as feature-complete. It is acceptable only when all of the following
hold on one exact candidate:

1. Every version 1.0 requirement ID in sections 5.1 through 5.8 has an
   implemented surface or an explicit non-applicable explanation consistent
   with the version 1.0 non-goals. No knowingly absent core feature is hidden
   behind a limitation.
2. A route-and-method inventory accounts for every current full-page, HTMX,
   direct-object, summary, count, canonical/metadata, mutation, and error
   surface. The complete applicable leakage matrix passes for visitor, member,
   group member, moderator, administrator, suspended, revoked, and stale-session
   states without revealing restricted existence or occupancy.
3. The pre-beta security gate passes: dependency and secret scans; CSRF,
   cookie, browser-header, OIDC return-path, injection, XSS, proxy-identity,
   limiter-bypass, and bounded-log checks. No known critical security or
   data-loss defect is admitted.
4. Designated test users can complete login, navigation, search, unread,
   topic/reply/edit, report, moderation, and administration journeys with
   ordinary HTML. Core journeys remain responsive and keyboard-accessible;
   HTMX enhancement preserves focus, errors, status, history, authorization,
   and submitted drafts.
5. Representative-data evidence covers every core page's bounded query count,
   pagination boundary, custom and generic PostgreSQL plan structure, pool and
   cancellation behavior, and coexistence with authenticated reads and writes.
   Beta.1 adds no scale or latency promise beyond the measured single-process
   deployment.
6. Authentik disable takes effect no later than the deployed revalidation
   interval, which is recorded with the release. Local role, group, suspension,
   mute, and target-session revocation takes effect on the next protected
   request. Neither path claims propagation faster than the evidence proves.
7. An actual supported alpha database is backed up, upgraded to the Beta.1
   schema with the application stopped, granted only the packaged runtime
   privileges, and smoke-tested. Failure and unknown migration outcomes use the
   documented inspect/forward-repair/restore decisions rather than an invented
   down-migration.
8. An initial backup/restore rehearsal creates a digested PostgreSQL backup and
   a non-secret configuration/release inventory, restores into a clean supported
   PostgreSQL instance, reapplies the packaged runtime grants, and passes
   readiness plus application smoke tests. Elapsed backup and recovery times
   and the actual failure domain are recorded.
9. Known limitations and residual risks are published. Same-host-only backup,
   absent scheduling/retention/alerting, or restricted test-user access may be
   declared Beta limitations only while every designated tester is told that
   Beta data is non-production and may be lost with the host. Restricted-content
   disclosure, authentication or authorization bypass, unrecoverable migration,
   and known application-caused data loss may not be admitted.
10. The admitted commit produces two byte-identical immutable packages and one
    tagged release artifact. Deployment uses that artifact, preserves an exact
    pre-upgrade backup and rollback record, and passes the Beta smoke matrix
    through Caddy before the owner is asked to confirm the real workflow.
11. The Beta deployment satisfies `OPS-006`. An existing account moved from a
    shared issuer retains its local user, role, memberships, content ownership,
    and audit history through one stopped, explicit identity rebind. The
    rebind revokes existing sessions and is itself audited. It never copies an
    unrelated Authentik application, user, signing key, database row, or
    instance secret into the dedicated deployment.

Beta.1 does not authorize public production use, unrestricted enrollment,
horizontal replicas, new version 2 features, a stable durability claim, or the
RC.1 artifact/migration freeze. Feedback discovered after Beta.1 admission is
corrected through reviewable Beta work before RC.1; feedback is not a circular
prerequisite for opening the first Beta to testers.

## 15. Stable 1.0 acceptance boundary

`1.0.0` requires:

- All version 1.0 requirements traced to passing evidence.
- No open critical or high-severity security defects.
- No known path that reveals restricted content to an unauthorized role.
- Successful fresh install, upgrade, backup, restore, and rollback rehearsal.
- An Authentik outage test showing fail-closed behavior without destroying
  existing sessions or content.
- Responsive and keyboard-accessible completion of all core flows.
- Operator documentation sufficient for a new operator to deploy and recover
  the service without undocumented commands.

## 16. Constraints and assumptions

- The forum remains one deployable Go service and one application PostgreSQL
  database in version 1.0. Its release stack also owns the required Caddy and
  Authentik processes plus Authentik's separate PostgreSQL database; those are
  deployment dependencies, not new forum application services.
- The stack's Caddy terminates TLS and routes the configured Board and
  Authentik hostnames to non-public upstreams.
- The stack's dedicated Authentik exposes the configured OIDC issuer and the
  required identity claims.
- SMTP, object storage, WebSockets, SCIM, and external search are not required
  for version 1.0.
- Production secrets are supplied at runtime and are never committed.
- The service initially targets one site and one identity issuer.

## 17. Open owner decisions

These do not block document creation but must be resolved before the affected
behavior changes:

1. Whether public areas are enabled for Beta test users or merely supported.
2. Content retention duration for soft-deleted posts and audit events.

The deployed Alpha already has an exact Authentik issuer/client and an audited
first-administrator issuer/subject. Those environment-specific identifiers stay
in protected configuration/audit state, not this public product document; the
Beta release preflight must verify that they remain unchanged without recording
secret or personal values.

For the initial Beta.1 deployment, the maximum delay between an Authentik
disable and protected forum access revocation retains the existing deployed
30-minute revalidation interval. This is an operator-configured maximum, not a
claim of immediate provider event propagation. Changing it requires an owner
decision and a new exact-boundary verification before deployment.

The initial rate-limit profile and new-account period are resolved by the AN-05
acceptance boundary. They remain operator configuration, not hard-coded product
law.

## 18. Change control

Requirement IDs are stable. A change that alters user-visible behavior,
permissions, identity authority, data retention, or release scope must update
this document before downstream implementation documents. Architecture cleanup
must not smuggle product behavior changes into code.
