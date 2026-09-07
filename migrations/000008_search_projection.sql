-- Lock risk: adding nullable columns and NOT VALID checks takes brief ACCESS
-- EXCLUSIVE locks. The five initially empty partial indexes still scan both
-- heaps to evaluate their predicates. Population and validation happen later
-- while application writers remain stopped.
-- Rewrite risk: nullable columns without defaults do not rewrite either heap.

ALTER TABLE public.topics
    ADD COLUMN search_vector tsvector,
    ADD COLUMN search_projection_version text,
    ADD CONSTRAINT topics_search_projection_current CHECK (
        (search_vector IS NULL AND search_projection_version IS NULL)
        OR (
            search_vector IS NOT NULL
            AND search_projection_version = 'search-v1-pg17-simple-u15-p2'
        )
    ) NOT VALID,
    ADD CONSTRAINT topics_search_created_finite CHECK (
        pg_catalog.isfinite(created_at)
    ) NOT VALID,
    ADD CONSTRAINT topics_search_id_positive CHECK (
        id > 0
    ) NOT VALID;

ALTER TABLE public.posts
    ADD COLUMN search_vector tsvector,
    ADD COLUMN search_projection_version text,
    ADD CONSTRAINT posts_search_projection_current CHECK (
        (search_vector IS NULL AND search_projection_version IS NULL)
        OR (
            search_vector IS NOT NULL
            AND search_projection_version = 'search-v1-pg17-simple-u15-p2'
        )
    ) NOT VALID,
    ADD CONSTRAINT posts_search_created_finite CHECK (
        pg_catalog.isfinite(created_at)
    ) NOT VALID,
    ADD CONSTRAINT posts_search_id_positive CHECK (
        id > 0
    ) NOT VALID;

CREATE TABLE public.search_projection_state (
    singleton boolean PRIMARY KEY DEFAULT true,
    target_version text NOT NULL,
    phase text NOT NULL DEFAULT 'topics',
    last_processed_id bigint,
    topics_converted_count bigint NOT NULL DEFAULT 0,
    posts_converted_count bigint NOT NULL DEFAULT 0,
    completed_at timestamp with time zone,
    CONSTRAINT search_projection_state_target_current CHECK (
        target_version = 'search-v1-pg17-simple-u15-p2'
    ),
    CONSTRAINT search_projection_state_shape CHECK (
        singleton
        AND phase IN ('topics', 'posts', 'complete')
        AND (last_processed_id IS NULL OR last_processed_id > 0)
        AND topics_converted_count >= 0
        AND posts_converted_count >= 0
        AND (
            (
                phase = 'topics'
                AND posts_converted_count = 0
                AND completed_at IS NULL
                AND ((topics_converted_count = 0) = (last_processed_id IS NULL))
            )
            OR (
                phase = 'posts'
                AND completed_at IS NULL
                AND ((posts_converted_count = 0) = (last_processed_id IS NULL))
            )
            OR (
                phase = 'complete'
                AND completed_at IS NOT NULL
                AND ((posts_converted_count = 0) = (last_processed_id IS NULL))
            )
        )
    ),
    CONSTRAINT search_projection_state_completion_finite CHECK (
        completed_at IS NULL OR pg_catalog.isfinite(completed_at)
    )
);

INSERT INTO public.search_projection_state (
    singleton,
    target_version,
    phase,
    last_processed_id,
    topics_converted_count,
    posts_converted_count,
    completed_at
)
VALUES (
    true,
    'search-v1-pg17-simple-u15-p2',
    'topics',
    NULL,
    0,
    0,
    NULL
);

CREATE INDEX topics_search_vector_current_idx
    ON public.topics USING gin (search_vector)
    WHERE deleted_at IS NULL
      AND search_projection_version = 'search-v1-pg17-simple-u15-p2';

CREATE INDEX posts_search_vector_current_idx
    ON public.posts USING gin (search_vector)
    WHERE deleted_at IS NULL
      AND redacted_at IS NULL
      AND search_projection_version = 'search-v1-pg17-simple-u15-p2';

CREATE INDEX topics_search_author_current_idx
    ON public.topics (author_id, created_at DESC, id DESC)
    WHERE deleted_at IS NULL
      AND search_projection_version = 'search-v1-pg17-simple-u15-p2';

CREATE INDEX posts_search_author_current_idx
    ON public.posts (author_id, created_at DESC, id DESC)
    WHERE deleted_at IS NULL
      AND redacted_at IS NULL
      AND search_projection_version = 'search-v1-pg17-simple-u15-p2';

CREATE INDEX posts_activity_current_idx
    ON public.posts (created_at DESC, id DESC)
    WHERE deleted_at IS NULL
      AND redacted_at IS NULL
      AND search_projection_version = 'search-v1-pg17-simple-u15-p2';

DROP TRIGGER topics_validate_post_state ON public.topics;
DROP TRIGGER posts_validate_topic_state ON public.posts;

CREATE CONSTRAINT TRIGGER topics_validate_post_state_insert
AFTER INSERT ON public.topics
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW
EXECUTE FUNCTION public.gotth_validate_topic_post_state();

CREATE CONSTRAINT TRIGGER topics_validate_post_state_update
AFTER UPDATE OF id, first_post_id, latest_post_id, reply_count, next_post_number ON public.topics
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW
WHEN (
    OLD.id IS DISTINCT FROM NEW.id
    OR OLD.first_post_id IS DISTINCT FROM NEW.first_post_id
    OR OLD.latest_post_id IS DISTINCT FROM NEW.latest_post_id
    OR OLD.reply_count IS DISTINCT FROM NEW.reply_count
    OR OLD.next_post_number IS DISTINCT FROM NEW.next_post_number
)
EXECUTE FUNCTION public.gotth_validate_topic_post_state();

CREATE CONSTRAINT TRIGGER topics_validate_post_state_delete
AFTER DELETE ON public.topics
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW
EXECUTE FUNCTION public.gotth_validate_topic_post_state();

CREATE CONSTRAINT TRIGGER posts_validate_topic_state_insert
AFTER INSERT ON public.posts
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW
EXECUTE FUNCTION public.gotth_validate_topic_post_state();

CREATE CONSTRAINT TRIGGER posts_validate_topic_state_update
AFTER UPDATE OF id, topic_id, post_number ON public.posts
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW
WHEN (
    OLD.id IS DISTINCT FROM NEW.id
    OR OLD.topic_id IS DISTINCT FROM NEW.topic_id
    OR OLD.post_number IS DISTINCT FROM NEW.post_number
)
EXECUTE FUNCTION public.gotth_validate_topic_post_state();

CREATE CONSTRAINT TRIGGER posts_validate_topic_state_delete
AFTER DELETE ON public.posts
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW
EXECUTE FUNCTION public.gotth_validate_topic_post_state();
