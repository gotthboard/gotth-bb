# Beta.1 route and authority inventory

This is the reviewed production route inventory for B1-00. Paths are shown
without `BASE_PATH`; the same route set must work at the empty and `/bb` base
paths. `optional` means the route may resolve a session to personalize or widen
an already authorized read, not that session state may weaken visitor policy.
Every dynamic response is non-cacheable when authenticated or error-bearing;
the gate verifies the exact emitted header for each state.

`Mutation` means durable PostgreSQL state may change. `Audit` means the same
transaction must append the immutable forum audit record. OIDC attempt/session
state and read markers are explicit mutations but are not moderation audits.

| Method | Pattern | Authority | CSRF | Cache class | DB / Mutation / Audit | Missing or denied |
| --- | --- | --- | --- | --- | --- | --- |
| `GET` | `/` | optional session | no | visitor dynamic or `private, no-store` | yes / no / no | bounded page or `503` |
| `GET` | `/areas/{slug}` | optional session plus area policy | no | visitor dynamic or `private, no-store` | yes / no / no | equivalent `404` |
| `GET` | `/topics/{topicID}` | optional session plus topic/area policy | no | visitor dynamic or `private, no-store` | yes / no / no | equivalent `404` |
| `GET` | `/health/live` | public | no | `no-store` | no / no / no | fixed `200` while process serves |
| `GET` | `/health/ready` | public bounded readiness | no | `no-store` | yes / no / no | fixed `503` without detail |
| `GET, HEAD` | `/static/app-3f1bf5d28948bd8391383ee7aba5e9cf14fc5b746449e93d996dff895406cacf.css` | public exact content address | no | one-year public immutable | no / no / no | `404` |
| `GET, HEAD` | `/static/app-4237ef90067eac5c030813c0722cfd222a7419a85169782c672d4c96f8727893.css` | public retained Beta.1.5 content address | no | one-year public immutable | no / no / no | `404` |
| `GET, HEAD` | `/static/app-d8e495881d927546f70f69915c1807efc8fb02c2bc22c9d2a98e87736a04e210.css` | public retained Beta.1.4 content address | no | one-year public immutable | no / no / no | `404` |
| `GET, HEAD` | `/static/app-3104ce3eede233f21f5a885d4fc547bcc33a24f8249e1d2757f0606a6d958251.css` | public retained Beta.1.1 content address | no | one-year public immutable | no / no / no | `404` |
| `GET, HEAD` | `/static/app-3faf03facd9c7083d4d359467a15860e45effe7a5a6c94aeb7c98f756993a6fa.css` | public retained Beta.1 content address | no | one-year public immutable | no / no / no | `404` |
| `GET, HEAD` | `/static/htmx-2.0.10.min.js` | public exact version | no | one-year public immutable | no / no / no | `404` |
| `GET, HEAD` | `/static/discovery-response-83d6c618d951879489e90e83d947a31d4211b8f565604c773346fd1ef0d4152b.js` | public exact content address | no | one-year public immutable | no / no / no | `404` |
| `GET, HEAD` | `/static/markdown-toolbar-9b94e2d14953039596b28abd1bf40cda34ebc0fcd910204606ca0f3862b36848.js` | public exact content address | no | one-year public immutable | no / no / no | `404` |
| `GET` | `/login` | public | no | `no-store` | yes / OIDC attempt / no | bounded `400`/`503` |
| `GET` | `/auth/callback` | one-time OIDC state | no | `no-store` | yes / identity plus session / no | fixed failed-login result |
| `GET` | `/auth/revalidate` | existing local session | no | `no-store` | yes / OIDC attempt / no | login or bounded failure |
| `POST` | `/logout` | current local session | yes | `no-store` | yes / session revoke / no | fixed recovery/error |
| `GET` | `/register` | public only when operator gate is enabled | no | `no-store` | no / no / no | `404` while disabled |
| `GET` | `/registration/admission/{mode}` | public exact current-mode probe | no | `no-store` | yes / no / no | empty `404`/`503` |
| `POST` | `/registration/intake/approval` | exact Authentik-signed assertion | external signed assertion | `no-store` | yes / bounded idempotent pending intake / no | empty bounded grammar/`202`/`503` |
| `GET` | `/setup` | designated current/revalidated member | no | `no-store` | yes / no / no | login/revalidate/closed result |
| `POST` | `/setup/administrator` | designated current/revalidated member | yes | `no-store` | yes / role, session / yes | fixed closed/denied/error |
| `GET` | `/rules` | public | no | `no-store` | yes / no / no | bounded `404`/`503` |
| `GET` | `/search` | optional session plus result policy | no | `private, no-store` | yes / no / no | bounded `400`/`503` |
| `GET` | `/activity` | optional session plus result policy | no | `private, no-store` | yes / no / no | bounded `400`/`503` |
| `GET` | `/posts/{postID}` | optional session plus post/area policy | no | `private, no-store` | yes / no / no | equivalent `404` |
| `GET` | `/topics/{topicID}/unread` | current/revalidated member plus topic policy | no | `private, no-store` | yes / no / no | equivalent `404` or canonical redirect |
| `POST` | `/topics/{topicID}/read` | current/revalidated member plus topic policy | yes | `private, no-store` | yes / read marker / no | equivalent `404`/fixed error |
| `GET` | `/topics/new` | current/revalidated member plus area policy | no | `no-store` | yes / no / no | login/revalidate/denied/not found |
| `POST` | `/topics/preview` | current/revalidated member plus area/content policy | yes | `private, no-store` | yes / no / no | field-safe bounded error |
| `POST` | `/topics` | current/revalidated member plus area/content/rate policy | yes | `private, no-store` | yes / topic, post, counter / no | field-safe `4xx`/bounded `5xx` |
| `POST` | `/topics/{topicID}/replies/preview` | current/revalidated member plus topic/content policy | yes | `private, no-store` | yes / no / no | equivalent `404`/field-safe error |
| `POST` | `/topics/{topicID}/replies` | current/revalidated member plus topic/content/rate policy | yes | `private, no-store` | yes / post, counter / no | equivalent `404`/field-safe error |
| `GET` | `/posts/{postID}/edit` | current/revalidated author plus post policy | no | `no-store` | yes / no / no | equivalent `404`/denied |
| `POST` | `/posts/{postID}/edit/preview` | current/revalidated author plus content policy | yes | `private, no-store` | yes / no / no | equivalent `404`/field-safe error |
| `POST` | `/posts/{postID}/edit` | current/revalidated author plus content policy | yes | `private, no-store` | yes / post / no | equivalent `404`/conflict/error |
| `POST` | `/posts/{postID}/delete` | current/revalidated author plus post policy | yes | `no-store` | yes / soft delete / no | equivalent `404`/conflict/error |
| `POST` | `/reports` | current/revalidated member plus target policy | yes | `no-store` | yes / report / no | equivalent target absence/field error |
| `GET` | `/moderation/reports` | current/revalidated moderator or administrator | no | `no-store` | yes / no / no | fixed `403` or bounded error |
| `GET` | `/moderation/reports/{reportID}` | current/revalidated moderator or administrator | no | `no-store` | yes / no / no | equivalent `404` |
| `POST` | `/moderation/reports/{reportID}/claim` | current/revalidated moderator or administrator | yes | `no-store` | yes / report / yes | equivalent `404`/conflict/error |
| `POST` | `/moderation/reports/{reportID}/notes` | current/revalidated moderator or administrator | yes | `no-store` | yes / note/report / yes | equivalent `404`/conflict/error |
| `POST` | `/moderation/reports/{reportID}/resolve` | current/revalidated moderator or administrator | yes | `no-store` | yes / report / yes | equivalent `404`/conflict/error |
| `POST` | `/moderation/reports/{reportID}/dismiss` | current/revalidated moderator or administrator | yes | `no-store` | yes / report / yes | equivalent `404`/conflict/error |
| `POST` | `/moderation/actions` | current/revalidated moderator or administrator plus target/action policy | yes | `no-store` | yes / target / yes | equivalent target absence/field error |
| `GET` | `/moderation/users/{userID}` | current/revalidated moderator or administrator | no | `no-store` | yes / no / no | equivalent `404`/fixed `403` |
| `POST` | `/moderation/users/{userID}/suspend` | current/revalidated moderator or administrator | yes | `no-store` | yes / suspension, sessions / yes | equivalent `404`/continuity/error |
| `POST` | `/moderation/users/{userID}/reinstate` | current/revalidated moderator or administrator | yes | `no-store` | yes / suspension / yes | equivalent `404`/conflict/error |
| `POST` | `/topics/{topicID}/lock` | current/revalidated moderator or administrator plus topic policy | yes | `no-store` | yes / topic / yes | equivalent `404`/conflict/error |
| `POST` | `/topics/{topicID}/unlock` | current/revalidated moderator or administrator plus topic policy | yes | `no-store` | yes / topic / yes | equivalent `404`/conflict/error |
| `POST` | `/topics/{topicID}/hide` | current/revalidated moderator or administrator plus topic policy | yes | `no-store` | yes / topic / yes | equivalent `404`/conflict/error |
| `POST` | `/topics/{topicID}/restore` | current/revalidated moderator or administrator plus topic policy | yes | `no-store` | yes / topic / yes | equivalent `404`/conflict/error |
| `GET` | `/admin` | current/revalidated administrator | no | `private, no-store` | yes / no / no | fixed `403`/bounded `503` |
| `GET` | `/admin/settings` | current/revalidated administrator | no | `private, no-store` | yes / no / no | fixed `403`/bounded `503` |
| `POST` | `/admin/settings` | current/revalidated administrator | yes | `private, no-store` | yes / settings / yes | fixed `403`/conflict/field error |
| `GET` | `/admin/accounts` | current/revalidated administrator | no | `private, no-store` | yes / no / no | fixed `403`/bounded `503` |
| `GET` | `/admin/registrations` | current/revalidated administrator | no | `private, no-store` | yes / no / no | fixed `403`/bounded `503` |
| `POST` | `/admin/registrations/{registrationID}/approve` | current/revalidated administrator | yes | `private, no-store` | yes / restrictive identity transition / yes | fixed `403`/conflict/remote error |
| `POST` | `/admin/registrations/{registrationID}/reject` | current/revalidated administrator | yes | `private, no-store` | yes / restrictive identity transition / yes | fixed `403`/conflict/remote error |
| `POST` | `/admin/registrations/{handle}/adopt` | current/revalidated administrator plus expiring signed handle | yes | `private, no-store` | yes / pending row / yes | fixed `403`/conflict/remote error |
| `GET` | `/admin/invitations` | current/revalidated administrator | no | `private, no-store` | yes / no / no | fixed `403`/bounded `503` |
| `POST` | `/admin/invitations` | current/revalidated administrator | yes | `private, no-store` | yes / invitation transition / yes | fixed `403`/conflict/remote or SMTP result |
| `POST` | `/admin/invitations/{handle}/revoke` | current/revalidated administrator plus expiring signed handle | yes | `private, no-store` | yes / invitation transition / yes | fixed `403`/conflict/remote result |
| `GET` | `/admin/accounts/{userID}` | current/revalidated administrator | no | `private, no-store` | yes / no / no | equivalent `404`/fixed `403` |
| `POST` | `/admin/accounts/{userID}/role` | current/revalidated administrator | yes | `private, no-store` | yes / role, sessions / yes | equivalent `404`/continuity/conflict |
| `POST` | `/admin/accounts/{userID}/identity/reconcile` | current/revalidated administrator | yes | `private, no-store` | yes / restrictive identity reconciliation / yes | fixed `403`/conflict/remote error |
| `POST` | `/admin/accounts/{userID}/groups/{groupID}` | current/revalidated administrator | yes | `private, no-store` | yes / membership / yes | equivalent `404`/conflict/error |
| `GET` | `/admin/groups` | current/revalidated administrator | no | `private, no-store` | yes / no / no | fixed `403`/bounded `503` |
| `POST` | `/admin/groups` | current/revalidated administrator | yes | `private, no-store` | yes / group / yes | fixed `403`/conflict/field error |
| `POST` | `/admin/groups/{groupID}` | current/revalidated administrator | yes | `private, no-store` | yes / group / yes | equivalent `404`/conflict/error |
| `GET` | `/admin/areas` | current/revalidated administrator | no | `private, no-store` | yes / no / no | fixed `403`/bounded `503` |
| `POST` | `/admin/areas` | current/revalidated administrator | yes | `private, no-store` | yes / area / yes | fixed `403`/conflict/field error |
| `GET` | `/admin/areas/{areaID}` | current/revalidated administrator | no | `private, no-store` | yes / no / no | equivalent `404`/fixed `403` |
| `POST` | `/admin/areas/{areaID}` | current/revalidated administrator | yes | `private, no-store` | yes / area / yes | equivalent `404`/conflict/error |
| `POST` | `/admin/areas/{areaID}/groups/{groupID}` | current/revalidated administrator | yes | `private, no-store` | yes / area membership / yes | equivalent `404`/conflict/error |
| `GET` | `/admin/control` | current/revalidated administrator | no | `private, no-store` | yes / no / no | fixed `403`/bounded `503` |
| `POST` | `/admin/control` | current/revalidated administrator | yes | `private, no-store` | yes / control settings / yes | fixed `403`/conflict/field error |
| `GET` | `/admin/accounts/{userID}/sessions` | current/revalidated administrator | no | `private, no-store` | yes / no / no | equivalent `404`/fixed `403` |
| `POST` | `/admin/sessions/{handle}/revoke` | current/revalidated administrator plus expiring session-bound handle | yes | `private, no-store` | yes / one local session / yes | fixed `403`/conflict/error |
| `POST` | `/admin/accounts/{userID}/sessions/revoke` | current/revalidated administrator plus target revision | yes | `private, no-store` | yes / all current local sessions / yes | equivalent `404`/conflict/error |
| `GET` | `/admin/email` | current/revalidated administrator | no | `private, no-store` | yes / no / no | fixed `403`/bounded `503` |
| `POST` | `/admin/email/test` | current/revalidated administrator | yes | `private, no-store` | yes / email-test state / yes | fixed `403`/rate limit/SMTP result |

All other method/path/query/raw-path forms go through the bounded not-found or
method-denial behavior and must not reach an inner session, body, or database
boundary when preflight rejects them. Future RSS, notification, related-topic,
API, federation, media, and attachment routes do not exist in version 1.0 and
are explicitly outside the applicable Beta leakage set.

## B1-09 route boundary

Every B1-09 administrator POST uses the existing current-session,
revalidation, CSRF, bounded-body, request-ID, private/no-store, and audit rules.
The signed intake route requires a narrowly amended inventory rule because its
authority is an external signed assertion rather than a browser cookie; that
exception must be exact and cannot weaken CSRF coverage for any browser POST.
