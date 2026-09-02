# Product requirements document

## Document control

| Field | Value |
| --- | --- |
| Product | GOTTH Board |
| Status | Draft for owner review |
| Document version | 0.2 |
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

A member granted the forum-local administrator role through the explicit
bootstrap/operator procedure or by an existing administrator. An administrator
manages areas, access rules, local roles and groups, local account state, and
site settings. Administrative changes are audited.

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
- **FORUM-003:** Members shall create topics in eligible areas and chronological
  replies inside eligible topics.
- **FORUM-004:** Version 1.0 replies shall be flat and chronological. Quotes may
  reference earlier posts without creating a reply tree.
- **FORUM-005:** Topics may be pinned, locked, moved, hidden, restored, or
  archived by authorized staff.
- **FORUM-006:** Topics and posts shall have stable identifiers and canonical
  URLs beneath the configured public base URL.
- **CONTENT-001:** Authors shall write Markdown and preview the sanitized
  rendered result before publication.
- **CONTENT-002:** Supported version 1.0 formatting shall include paragraphs,
  emphasis, lists, links, quotes, fenced code, inline code, and basic emoji.
- **CONTENT-003:** Raw HTML shall be disabled or sanitized to the documented
  allowlist.
- **CONTENT-004:** Authors shall edit their own visible content and see an
  edited timestamp.
- **CONTENT-005:** Author deletion shall be soft deletion governed by forum
  policy; hard purge is an administrative retention action.
- **CONTENT-006:** Validation failure shall preserve submitted content and
  return field-specific errors.
- **CONTENT-007:** Version 1.0 shall not include attachments, polls, arbitrary
  embeds, nested replies, or private messaging.

### 5.4 Reading and discovery

- **READ-001:** The forum shall provide an area index, topic lists, topic
  pages, recent activity, and conventional pagination.
- **READ-002:** Signed-in members shall have new/unread indicators and a jump
  to first unread action.
- **READ-003:** PostgreSQL full-text search shall support text, author, area,
  and date filters.
- **READ-004:** Search and activity queries shall apply the same access
  predicate as direct reads before rows are returned or counted.
- **READ-005:** Pages shall provide breadcrumbs and canonical URLs that include
  the configured external base path, including a root deployment.

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

## 8. Stable 1.0 acceptance boundary

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

## 9. Constraints and assumptions

- The forum is a single deployable Go service and PostgreSQL database in
  version 1.0.
- Caddy terminates TLS and routes `bb.alhstudios.com` to the service.
- Authentik exposes a reachable OIDC issuer and the required identity claims.
- SMTP, object storage, WebSockets, SCIM, and external search are not required
  for version 1.0.
- Production secrets are supplied at runtime and are never committed.
- The service initially targets one site and one identity issuer.

## 10. Open owner decisions

These do not block document creation but must be resolved before the affected
implementation begins:

1. Exact Authentik issuer URL and client/application identifier.
2. Exact issuer/subject pair for the first audited local administrator grant.
3. Maximum delay between an Authentik disable and forum access revocation.
4. Whether public areas are enabled at first deployment or merely supported.
5. Content retention duration for soft-deleted posts and audit events.
6. Initial rate-limit values and new-account period.

## 11. Change control

Requirement IDs are stable. A change that alters user-visible behavior,
permissions, identity authority, data retention, or release scope must update
this document before downstream implementation documents. Architecture cleanup
must not smuggle product behavior changes into code.
