-- Lock risk: ACCESS EXCLUSIVE while adding constraints/triggers and while the
-- bounded alpha data set is backfilled. Deploy only after a verified backup.
-- Rewrite risk: one posts-table rewrite when thread_path becomes NOT NULL.

ALTER TABLE public.posts
    ADD COLUMN parent_post_id bigint,
    ADD COLUMN thread_path integer[];

UPDATE public.posts AS post
SET parent_post_id = CASE WHEN post.id = topic.first_post_id THEN NULL ELSE topic.first_post_id END,
    thread_path = CASE
        WHEN post.id = topic.first_post_id THEN ARRAY[1]::integer[]
        ELSE ARRAY[1, post.post_number]::integer[]
    END
FROM public.topics AS topic
WHERE topic.id = post.topic_id;

ALTER TABLE public.posts
    ALTER COLUMN thread_path SET NOT NULL,
    ADD CONSTRAINT posts_topic_id_id_unique UNIQUE (topic_id, id),
    ADD CONSTRAINT posts_parent_same_topic_fk
        FOREIGN KEY (topic_id, parent_post_id)
        REFERENCES public.posts (topic_id, id)
        ON DELETE RESTRICT
        DEFERRABLE INITIALLY DEFERRED,
    ADD CONSTRAINT posts_thread_path_shape CHECK (
        cardinality(thread_path) BETWEEN 1 AND 32
        AND thread_path[1] = 1
        AND array_position(thread_path, NULL) IS NULL
        AND (
            (post_number = 1 AND parent_post_id IS NULL AND thread_path = ARRAY[1]::integer[])
            OR
            (post_number > 1 AND parent_post_id IS NOT NULL AND cardinality(thread_path) >= 2)
        )
    );

CREATE INDEX posts_topic_thread_order_idx
    ON public.posts (topic_id, thread_path);

CREATE FUNCTION public.gotth_prepare_post_thread()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public
AS $$
DECLARE
    root_post_id bigint;
    parent_path integer[];
    expected_path integer[];
BEGIN
    IF NEW.post_number = 1 THEN
        IF NEW.parent_post_id IS NOT NULL THEN
            RAISE EXCEPTION 'topic root cannot have a parent' USING ERRCODE = 'check_violation';
        END IF;
        expected_path := ARRAY[1]::integer[];
    ELSE
        -- Compatibility for the released alpha.1 binary: its reply INSERT does
        -- not name a parent. During rollback, map those replies to the root.
        IF NEW.parent_post_id IS NULL THEN
            SELECT topic.first_post_id
            INTO root_post_id
            FROM public.topics AS topic
            WHERE topic.id = NEW.topic_id;

            IF NOT FOUND THEN
                RAISE EXCEPTION 'reply topic does not exist' USING ERRCODE = 'foreign_key_violation';
            END IF;
            NEW.parent_post_id := root_post_id;
        END IF;

        SELECT parent.thread_path
        INTO parent_path
        FROM public.posts AS parent
        WHERE parent.topic_id = NEW.topic_id
          AND parent.id = NEW.parent_post_id;

        IF NOT FOUND THEN
            RAISE EXCEPTION 'reply parent does not belong to topic' USING ERRCODE = 'foreign_key_violation';
        END IF;
        expected_path := parent_path || NEW.post_number;
    END IF;

    IF cardinality(expected_path) > 32 THEN
        RAISE EXCEPTION 'reply depth exceeds 32 levels' USING ERRCODE = 'check_violation';
    END IF;
    IF NEW.thread_path IS NULL THEN
        NEW.thread_path := expected_path;
    ELSIF NEW.thread_path IS DISTINCT FROM expected_path THEN
        RAISE EXCEPTION 'post thread path is inconsistent' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER posts_prepare_thread
BEFORE INSERT ON public.posts
FOR EACH ROW
EXECUTE FUNCTION public.gotth_prepare_post_thread();

CREATE OR REPLACE FUNCTION public.gotth_reject_post_identity_change()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public
AS $$
BEGIN
    IF NEW.topic_id IS DISTINCT FROM OLD.topic_id
       OR NEW.post_number IS DISTINCT FROM OLD.post_number
       OR NEW.parent_post_id IS DISTINCT FROM OLD.parent_post_id
       OR NEW.thread_path IS DISTINCT FROM OLD.thread_path THEN
        RAISE EXCEPTION 'post topic, number, parent, and thread path are immutable' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER posts_identity_immutable ON public.posts;

CREATE TRIGGER posts_identity_immutable
BEFORE UPDATE OF topic_id, post_number, parent_post_id, thread_path ON public.posts
FOR EACH ROW
EXECUTE FUNCTION public.gotth_reject_post_identity_change();
