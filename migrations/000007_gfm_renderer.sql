-- Lock risk: ACCESS EXCLUSIVE is taken briefly while adding the NOT VALID
-- renderer constraint. This schema transaction performs no posts scan or
-- validation while retaining that lock. The mandatory release re-render phase
-- performs its stale-row scan and final validation in later transactions after
-- the old application has been stopped. The new constraint rejects
-- stale-version writes immediately even before existing rows are validated.
-- Rewrite risk: no heap rewrite occurs in this SQL migration. Existing
-- ordinary post rows are re-rendered later by the bounded release migration
-- command from their canonical Markdown source.

CREATE TABLE public.content_renderer_state (
    singleton boolean PRIMARY KEY DEFAULT true,
    target_version text NOT NULL,
    converted_count bigint NOT NULL DEFAULT 0,
    last_processed_post_id bigint,
    completed_at timestamp with time zone,
    CONSTRAINT content_renderer_state_singleton CHECK (singleton),
    CONSTRAINT content_renderer_state_target_length CHECK (char_length(target_version) BETWEEN 1 AND 64),
    CONSTRAINT content_renderer_state_converted_nonnegative CHECK (converted_count >= 0),
    CONSTRAINT content_renderer_state_cursor_progress CHECK (
        (converted_count = 0 AND last_processed_post_id IS NULL)
        OR (converted_count > 0 AND last_processed_post_id IS NOT NULL)
    )
);

INSERT INTO public.content_renderer_state (singleton, target_version, completed_at)
VALUES (
    true,
    'goldmark-v1.8.5-gfm-bluemonday-v1.0.27-p2',
    NULL
);

ALTER TABLE public.posts
    ADD CONSTRAINT posts_renderer_version_current CHECK (
        renderer_version = 'goldmark-v1.8.5-gfm-bluemonday-v1.0.27-p2'
        OR (
            renderer_version = 'goldmark-v1.8.5-bluemonday-v1.0.27-p1-preserved'
            AND redacted_at IS NULL
        )
        OR (
            renderer_version = 'moderation-redaction-v1'
            AND redacted_at IS NOT NULL
        )
    ) NOT VALID;
