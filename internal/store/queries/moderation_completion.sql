-- name: LockTopicForExtendedModeration :one
SELECT topic.id, topic.area_id, topic.state, topic.pinned_at, topic.created_at, topic.updated_at
FROM public.topics AS topic
WHERE topic.id = sqlc.arg(topic_id) AND topic.deleted_at IS NULL
FOR UPDATE OF topic;

-- name: GetDestinationAreaBySlug :one
SELECT area.id, area.slug, area.name, area.posting_mode
FROM public.areas AS area
WHERE area.slug = sqlc.arg(area_slug);

-- name: LockAreaForTopicMove :one
SELECT area.id, area.slug, area.name, area.posting_mode
FROM public.areas AS area
WHERE area.id = sqlc.arg(area_id)
FOR UPDATE OF area;

-- name: ChangeTopicPinAndAudit :one
WITH changed AS (
    UPDATE public.topics AS topic
    SET pinned_at = sqlc.narg(resulting_pinned_at),
        updated_at = GREATEST(sqlc.arg(at_time)::timestamptz, topic.updated_at)
    WHERE topic.id = sqlc.arg(topic_id)
      AND topic.pinned_at IS NOT DISTINCT FROM sqlc.narg(previous_pinned_at)::timestamptz
    RETURNING topic.id, topic.pinned_at, topic.updated_at
), audit AS (
    INSERT INTO public.moderation_actions (
        actor_kind, actor_user_id, target_type, target_topic_id, action_type,
        reason, previous_state, resulting_state, request_id, created_at
    )
    SELECT 'forum_user', sqlc.arg(actor_user_id), 'topic', changed.id,
           sqlc.arg(action_type), sqlc.arg(reason),
           jsonb_build_object('pinned_at', sqlc.narg(previous_pinned_at)::timestamptz),
           jsonb_build_object('pinned_at', changed.pinned_at),
           sqlc.arg(request_id), changed.updated_at
    FROM changed RETURNING id, target_topic_id
)
SELECT changed.id AS topic_id, changed.pinned_at, changed.updated_at, audit.id AS audit_id
FROM changed JOIN audit ON audit.target_topic_id = changed.id;

-- name: MoveTopicAndAudit :one
WITH changed AS (
    UPDATE public.topics AS topic
    SET area_id = sqlc.arg(resulting_area_id),
        slug = NULL,
        updated_at = GREATEST(sqlc.arg(at_time)::timestamptz, topic.updated_at)
    WHERE topic.id = sqlc.arg(topic_id) AND topic.area_id = sqlc.arg(previous_area_id)
    RETURNING topic.id, topic.area_id, topic.updated_at
), audit AS (
    INSERT INTO public.moderation_actions (
        actor_kind, actor_user_id, target_type, target_topic_id, action_type,
        reason, previous_state, resulting_state, request_id, created_at
    )
    SELECT 'forum_user', sqlc.arg(actor_user_id), 'topic', changed.id, 'move_topic',
           sqlc.arg(reason), jsonb_build_object('area_id', sqlc.arg(previous_area_id)::bigint),
           jsonb_build_object('area_id', changed.area_id), sqlc.arg(request_id), changed.updated_at
    FROM changed RETURNING id, target_topic_id
)
SELECT changed.id AS topic_id, changed.area_id, changed.updated_at, audit.id AS audit_id
FROM changed JOIN audit ON audit.target_topic_id = changed.id;

-- name: LockPostForModeration :one
SELECT post.id, post.topic_id, post.author_id, post.revision, post.deleted_at,
       post.deleted_by, post.deletion_reason, post.redacted_at, post.redacted_by,
       post.redaction_reason, post.created_at, post.updated_at
FROM public.posts AS post
JOIN public.topics AS topic ON topic.id = post.topic_id
WHERE post.id = sqlc.arg(post_id) AND topic.deleted_at IS NULL
FOR UPDATE OF post;

-- name: ChangePostVisibilityAndAudit :one
WITH changed AS (
    UPDATE public.posts AS post
    SET deleted_at = sqlc.narg(resulting_deleted_at),
        deleted_by = sqlc.narg(resulting_deleted_by),
        deletion_reason = sqlc.narg(resulting_reason),
        updated_at = GREATEST(sqlc.arg(at_time)::timestamptz, post.updated_at)
    WHERE post.id = sqlc.arg(post_id)
      AND post.redacted_at IS NULL
      AND post.deleted_at IS NOT DISTINCT FROM sqlc.narg(previous_deleted_at)::timestamptz
    RETURNING post.id, post.topic_id, post.deleted_at, post.deleted_by, post.updated_at
), audit AS (
    INSERT INTO public.moderation_actions (
        actor_kind, actor_user_id, target_type, target_post_id, action_type,
        reason, previous_state, resulting_state, request_id, created_at
    )
    SELECT 'forum_user', sqlc.arg(actor_user_id), 'post', changed.id,
           sqlc.arg(action_type), sqlc.arg(audit_reason),
           jsonb_build_object('deleted_at', sqlc.narg(previous_deleted_at)::timestamptz,
                              'deleted_by', sqlc.narg(previous_deleted_by)::bigint),
           jsonb_build_object('deleted_at', changed.deleted_at, 'deleted_by', changed.deleted_by),
           sqlc.arg(request_id), changed.updated_at
    FROM changed RETURNING id, target_post_id
)
SELECT changed.id AS post_id, changed.topic_id, changed.deleted_at,
       changed.deleted_by, changed.updated_at, audit.id AS audit_id
FROM changed JOIN audit ON audit.target_post_id = changed.id;

-- name: RedactPostAndAudit :one
WITH changed AS (
    UPDATE public.posts AS post
    SET markdown_source = '[Content removed by moderation]',
        rendered_html = '<p>Content removed by moderation.</p>',
        renderer_version = 'moderation-redaction-v1',
        revision = post.revision + 1,
        edited_at = GREATEST(sqlc.arg(at_time)::timestamptz, post.updated_at),
        deleted_at = GREATEST(sqlc.arg(at_time)::timestamptz, post.updated_at),
        deleted_by = sqlc.arg(actor_user_id), deletion_reason = sqlc.arg(reason),
        redacted_at = GREATEST(sqlc.arg(at_time)::timestamptz, post.updated_at),
        redacted_by = sqlc.arg(actor_user_id), redaction_reason = sqlc.arg(reason),
        updated_at = GREATEST(sqlc.arg(at_time)::timestamptz, post.updated_at)
    WHERE post.id = sqlc.arg(post_id) AND post.deleted_at IS NULL AND post.redacted_at IS NULL
    RETURNING post.id, post.topic_id, post.revision, post.redacted_at, post.redacted_by, post.updated_at
), audit AS (
    INSERT INTO public.moderation_actions (
        actor_kind, actor_user_id, target_type, target_post_id, action_type,
        reason, previous_state, resulting_state, request_id, created_at
    )
    SELECT 'forum_user', sqlc.arg(actor_user_id), 'post', changed.id, 'redact_post',
           sqlc.arg(reason), jsonb_build_object('revision', sqlc.arg(previous_revision)::integer, 'redacted', false),
           jsonb_build_object('revision', changed.revision, 'redacted', true),
           sqlc.arg(request_id), changed.updated_at
    FROM changed RETURNING id, target_post_id
)
SELECT changed.id AS post_id, changed.topic_id, changed.revision, changed.redacted_at,
       changed.redacted_by, changed.updated_at, audit.id AS audit_id
FROM changed JOIN audit ON audit.target_post_id = changed.id;

-- name: WarnUserAndAudit :one
WITH warned AS (
    INSERT INTO public.user_warnings (user_id, warned_by, reason, created_at)
    VALUES (sqlc.arg(user_id), sqlc.arg(actor_user_id), sqlc.arg(reason), sqlc.arg(at_time))
    RETURNING id, user_id, warned_by, created_at
), audit AS (
    INSERT INTO public.moderation_actions (
        actor_kind, actor_user_id, target_type, target_user_id, action_type,
        reason, previous_state, resulting_state, request_id, created_at
    )
    SELECT 'forum_user', warned.warned_by, 'user', warned.user_id, 'warn_user',
           sqlc.arg(reason), '{}'::jsonb, jsonb_build_object('warning_id', warned.id),
           sqlc.arg(request_id), warned.created_at
    FROM warned RETURNING id, target_user_id
)
SELECT warned.id AS warning_id, warned.user_id, warned.created_at, audit.id AS audit_id
FROM warned JOIN audit ON audit.target_user_id = warned.user_id;

-- name: MuteUserAndAudit :one
WITH changed AS (
    UPDATE public.users AS target
    SET muted_until = sqlc.arg(muted_until),
        updated_at = GREATEST(sqlc.arg(at_time)::timestamptz, target.updated_at)
    WHERE target.id = sqlc.arg(user_id)
      AND (target.muted_until IS NULL OR target.muted_until <= sqlc.arg(observed_at)::timestamptz)
    RETURNING target.id, target.muted_until, target.updated_at
), audit AS (
    INSERT INTO public.moderation_actions (
        actor_kind, actor_user_id, target_type, target_user_id, action_type,
        reason, previous_state, resulting_state, request_id, created_at
    )
    SELECT 'forum_user', sqlc.arg(actor_user_id), 'user', changed.id, 'mute_user',
           sqlc.arg(reason), jsonb_build_object('muted_until', sqlc.narg(previous_muted_until)::timestamptz),
           jsonb_build_object('muted_until', changed.muted_until),
           sqlc.arg(request_id), changed.updated_at
    FROM changed RETURNING id, target_user_id
)
SELECT changed.id AS user_id, changed.muted_until, changed.updated_at, audit.id AS audit_id
FROM changed JOIN audit ON audit.target_user_id = changed.id;
