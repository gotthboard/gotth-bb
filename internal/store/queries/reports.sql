-- name: CountActiveReportsByReporter :one
SELECT count(*)::integer
FROM public.reports
WHERE reported_by = sqlc.arg(reported_by)
  AND status IN ('open', 'in_review');

-- name: CreateTopicReport :one
INSERT INTO public.reports (reported_by, topic_id, reason, created_at, updated_at)
SELECT sqlc.arg(reported_by), topic.id, sqlc.arg(reason), sqlc.arg(at_time), sqlc.arg(at_time)
FROM public.topics AS topic
JOIN public.areas AS area ON area.id = topic.area_id
WHERE topic.id = sqlc.arg(topic_id)
  AND topic.deleted_at IS NULL
  AND (sqlc.arg(is_staff)::boolean OR topic.state <> 'hidden')
  AND (
      sqlc.arg(is_staff)::boolean
      OR area.visibility = 'public'
      OR (sqlc.arg(is_member)::boolean AND area.visibility = 'authenticated')
      OR (
          sqlc.arg(is_member)::boolean
          AND area.visibility = 'groups'
          AND EXISTS (
              SELECT 1 FROM public.area_groups AS membership
              WHERE membership.area_id = area.id
                AND membership.group_id = ANY(sqlc.arg(group_ids)::bigint[])
          )
      )
  )
RETURNING id, reported_by, topic_id, post_id, user_id, status, created_at;

-- name: CreatePostReport :one
INSERT INTO public.reports (reported_by, post_id, reason, created_at, updated_at)
SELECT sqlc.arg(reported_by), post.id, sqlc.arg(reason), sqlc.arg(at_time), sqlc.arg(at_time)
FROM public.posts AS post
JOIN public.topics AS topic ON topic.id = post.topic_id
JOIN public.areas AS area ON area.id = topic.area_id
WHERE post.id = sqlc.arg(post_id)
  AND post.deleted_at IS NULL
  AND topic.deleted_at IS NULL
  AND (sqlc.arg(is_staff)::boolean OR topic.state <> 'hidden')
  AND (
      sqlc.arg(is_staff)::boolean
      OR area.visibility = 'public'
      OR (sqlc.arg(is_member)::boolean AND area.visibility = 'authenticated')
      OR (
          sqlc.arg(is_member)::boolean
          AND area.visibility = 'groups'
          AND EXISTS (
              SELECT 1 FROM public.area_groups AS membership
              WHERE membership.area_id = area.id
                AND membership.group_id = ANY(sqlc.arg(group_ids)::bigint[])
          )
      )
  )
RETURNING id, reported_by, topic_id, post_id, user_id, status, created_at;

-- name: CreateUserReport :one
INSERT INTO public.reports (reported_by, user_id, reason, created_at, updated_at)
SELECT sqlc.arg(reported_by), target.id, sqlc.arg(reason), sqlc.arg(at_time), sqlc.arg(at_time)
FROM public.users AS target
WHERE target.id = sqlc.arg(user_id)
  AND target.id <> sqlc.arg(reported_by)
  AND EXISTS (
      SELECT 1
      FROM public.posts AS post
      JOIN public.topics AS topic ON topic.id = post.topic_id
      JOIN public.areas AS area ON area.id = topic.area_id
      WHERE post.author_id = target.id
        AND post.deleted_at IS NULL
        AND topic.deleted_at IS NULL
        AND (sqlc.arg(is_staff)::boolean OR topic.state <> 'hidden')
        AND (
            sqlc.arg(is_staff)::boolean
            OR area.visibility = 'public'
            OR (sqlc.arg(is_member)::boolean AND area.visibility = 'authenticated')
            OR (
                sqlc.arg(is_member)::boolean
                AND area.visibility = 'groups'
                AND EXISTS (
                    SELECT 1 FROM public.area_groups AS membership
                    WHERE membership.area_id = area.id
                      AND membership.group_id = ANY(sqlc.arg(group_ids)::bigint[])
                )
            )
        )
  )
RETURNING id, reported_by, topic_id, post_id, user_id, status, created_at;

-- name: ListActiveReportsForModeration :many
SELECT
    report.id,
    report.reason,
    report.status,
    report.created_at,
    reporter.display_name AS reporter_display_name,
    report.assigned_to,
    assignee.display_name AS assignee_display_name,
    CASE WHEN report.topic_id IS NOT NULL THEN 'topic'
         WHEN report.post_id IS NOT NULL THEN 'post'
         ELSE 'user' END::text AS target_type,
    COALESCE(report.topic_id, report.post_id, report.user_id)::bigint AS target_id,
    COALESCE(topic.title, post_topic.title, target_user.display_name)::text AS target_label,
    count(*) OVER ()::bigint AS total_active
FROM public.reports AS report
JOIN public.users AS reporter ON reporter.id = report.reported_by
LEFT JOIN public.users AS assignee ON assignee.id = report.assigned_to
LEFT JOIN public.topics AS topic ON topic.id = report.topic_id
LEFT JOIN public.posts AS post ON post.id = report.post_id
LEFT JOIN public.topics AS post_topic ON post_topic.id = post.topic_id
LEFT JOIN public.users AS target_user ON target_user.id = report.user_id
WHERE report.status IN ('open', 'in_review')
  AND EXISTS (
      SELECT 1 FROM public.users AS actor
      WHERE actor.id = sqlc.arg(actor_user_id)
        AND actor.role = sqlc.arg(actor_role)
        AND actor.role IN ('moderator', 'administrator')
        AND (actor.suspended_at IS NULL OR actor.suspended_at > sqlc.arg(observed_at)::timestamptz OR actor.suspended_until <= sqlc.arg(observed_at)::timestamptz)
        AND (actor.muted_until IS NULL OR actor.muted_until <= sqlc.arg(observed_at)::timestamptz)
  )
ORDER BY CASE report.status WHEN 'open' THEN 0 ELSE 1 END,
         report.created_at,
         report.id
LIMIT sqlc.arg(page_limit)::integer
OFFSET sqlc.arg(page_offset)::integer;

-- name: GetReportForModeration :one
SELECT
    report.id,
    report.reported_by,
    reporter.display_name AS reporter_display_name,
    report.reason,
    report.status,
    report.assigned_to,
    assignee.display_name AS assignee_display_name,
    report.resolution,
    report.resolved_by,
    resolver.display_name AS resolver_display_name,
    report.created_at,
    report.updated_at,
    report.resolved_at,
    CASE WHEN report.topic_id IS NOT NULL THEN 'topic'
         WHEN report.post_id IS NOT NULL THEN 'post'
         ELSE 'user' END::text AS target_type,
    COALESCE(report.topic_id, report.post_id, report.user_id)::bigint AS target_id,
    CASE WHEN report.user_id IS NULL THEN COALESCE(report.topic_id, post.topic_id)
         ELSE 0::bigint END AS target_topic_id,
    CASE WHEN report.post_id IS NULL THEN 0::bigint
         ELSE (
             SELECT 1 + (count(*) - 1) / 25
             FROM public.posts AS ranked_post
             WHERE ranked_post.topic_id = post.topic_id
               AND ranked_post.thread_path <= post.thread_path
         ) END::bigint AS target_page,
    COALESCE(topic.title, post_topic.title, target_user.display_name)::text AS target_label
FROM public.reports AS report
JOIN public.users AS reporter ON reporter.id = report.reported_by
LEFT JOIN public.users AS assignee ON assignee.id = report.assigned_to
LEFT JOIN public.users AS resolver ON resolver.id = report.resolved_by
LEFT JOIN public.topics AS topic ON topic.id = report.topic_id
LEFT JOIN public.posts AS post ON post.id = report.post_id
LEFT JOIN public.topics AS post_topic ON post_topic.id = post.topic_id
LEFT JOIN public.users AS target_user ON target_user.id = report.user_id
WHERE report.id = sqlc.arg(report_id)
  AND EXISTS (
      SELECT 1 FROM public.users AS actor
      WHERE actor.id = sqlc.arg(actor_user_id)
        AND actor.role = sqlc.arg(actor_role)
        AND actor.role IN ('moderator', 'administrator')
        AND (actor.suspended_at IS NULL OR actor.suspended_at > sqlc.arg(observed_at)::timestamptz OR actor.suspended_until <= sqlc.arg(observed_at)::timestamptz)
        AND (actor.muted_until IS NULL OR actor.muted_until <= sqlc.arg(observed_at)::timestamptz)
  );

-- name: ListReportNotes :many
SELECT report.id AS authorized_report_id,
       note.id,
       note.author_id,
       author.display_name AS author_display_name,
       note.body,
       note.created_at
FROM public.reports AS report
JOIN public.users AS actor
  ON actor.id = sqlc.arg(actor_user_id)
 AND actor.role = sqlc.arg(actor_role)
 AND actor.role IN ('moderator', 'administrator')
 AND (actor.suspended_at IS NULL OR actor.suspended_at > sqlc.arg(observed_at)::timestamptz OR actor.suspended_until <= sqlc.arg(observed_at)::timestamptz)
 AND (actor.muted_until IS NULL OR actor.muted_until <= sqlc.arg(observed_at)::timestamptz)
LEFT JOIN public.report_notes AS note ON note.report_id = report.id
LEFT JOIN public.users AS author ON author.id = note.author_id
WHERE report.id = sqlc.arg(report_id)
ORDER BY note.created_at, note.id;

-- name: LockReportForModeration :one
SELECT id, status, assigned_to, created_at, updated_at
FROM public.reports
WHERE id = sqlc.arg(report_id)
FOR UPDATE;

-- name: ClaimReportAndAudit :one
WITH changed AS (
    UPDATE public.reports AS report
    SET status = 'in_review', assigned_to = sqlc.arg(actor_user_id),
        updated_at = GREATEST(sqlc.arg(at_time)::timestamptz, report.updated_at)
    WHERE report.id = sqlc.arg(report_id) AND report.status = 'open' AND report.assigned_to IS NULL
    RETURNING report.id, report.status, report.assigned_to, report.updated_at
), audit AS (
    INSERT INTO public.moderation_actions (
        actor_kind, actor_user_id, target_type, target_report_id, action_type,
        previous_state, resulting_state, request_id, created_at
    )
    SELECT 'forum_user', sqlc.arg(actor_user_id), 'report', changed.id, 'assign_report',
           jsonb_build_object('status', 'open', 'assigned_to', NULL),
           jsonb_build_object('status', changed.status, 'assigned_to', changed.assigned_to),
           sqlc.arg(request_id), changed.updated_at
    FROM changed
    RETURNING id, target_report_id
)
SELECT changed.id AS report_id, changed.status, changed.assigned_to,
       changed.updated_at, audit.id AS audit_id
FROM changed JOIN audit ON audit.target_report_id = changed.id;

-- name: AddReportNoteAndAudit :one
WITH inserted AS (
    INSERT INTO public.report_notes (report_id, author_id, body, created_at)
    SELECT report.id, sqlc.arg(actor_user_id), sqlc.arg(body), sqlc.arg(at_time)
    FROM public.reports AS report
    WHERE report.id = sqlc.arg(report_id)
      AND report.status = 'in_review'
      AND report.assigned_to IS NOT NULL
    RETURNING report_notes.id, report_notes.report_id,
              report_notes.author_id, report_notes.created_at
), audit AS (
    INSERT INTO public.moderation_actions (
        actor_kind, actor_user_id, target_type, target_report_id, action_type,
        reason, previous_state, resulting_state, request_id, created_at
    )
    SELECT 'forum_user', inserted.author_id, 'report', inserted.report_id, 'note_report',
           sqlc.arg(body), '{}'::jsonb, jsonb_build_object('note_id', inserted.id),
           sqlc.arg(request_id), inserted.created_at
    FROM inserted
    RETURNING id, target_report_id
)
SELECT inserted.id AS note_id, inserted.report_id, inserted.author_id,
       inserted.created_at, audit.id AS audit_id
FROM inserted JOIN audit ON audit.target_report_id = inserted.report_id;

-- name: FinishReportAndAudit :one
WITH changed AS (
    UPDATE public.reports AS report
    SET status = sqlc.arg(resulting_status), resolution = sqlc.arg(resolution),
        resolved_by = sqlc.arg(actor_user_id),
        resolved_at = GREATEST(sqlc.arg(at_time)::timestamptz, report.updated_at),
        updated_at = GREATEST(sqlc.arg(at_time)::timestamptz, report.updated_at)
    WHERE report.id = sqlc.arg(report_id)
      AND report.status = 'in_review'
      AND report.assigned_to IS NOT NULL
    RETURNING report.id, report.status, report.assigned_to, report.resolution,
              report.resolved_by, report.resolved_at, report.updated_at
), audit AS (
    INSERT INTO public.moderation_actions (
        actor_kind, actor_user_id, target_type, target_report_id, action_type,
        reason, previous_state, resulting_state, request_id, created_at
    )
    SELECT 'forum_user', sqlc.arg(actor_user_id), 'report', changed.id,
           sqlc.arg(action_type), changed.resolution,
           jsonb_build_object('status', 'in_review', 'assigned_to', changed.assigned_to),
           jsonb_build_object('status', changed.status, 'assigned_to', changed.assigned_to,
                              'resolved_by', changed.resolved_by),
           sqlc.arg(request_id), changed.updated_at
    FROM changed
    RETURNING id, target_report_id
)
SELECT changed.id AS report_id, changed.status, changed.assigned_to,
       changed.resolved_by, changed.resolved_at, changed.updated_at, audit.id AS audit_id
FROM changed JOIN audit ON audit.target_report_id = changed.id;
