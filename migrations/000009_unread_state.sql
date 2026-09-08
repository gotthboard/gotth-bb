-- Lock risk: the NOT VALID check takes a brief ACCESS EXCLUSIVE lock,
-- validation scans topic_reads, and the regular partial index scans posts and
-- can block concurrent writers. Run this ordinary atomic migration while the
-- application is stopped.
-- Rewrite risk: none. No marker row is inserted, updated, or inferred.

ALTER TABLE public.topic_reads
    ADD CONSTRAINT topic_reads_read_at_finite CHECK (
        pg_catalog.isfinite(read_at)
    ) NOT VALID;

ALTER TABLE public.topic_reads
    VALIDATE CONSTRAINT topic_reads_read_at_finite;

CREATE INDEX posts_topic_unread_visible_idx
    ON public.posts (topic_id, post_number)
    INCLUDE (author_id)
    WHERE deleted_at IS NULL
      AND redacted_at IS NULL;
