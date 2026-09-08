-- Lock risk: adding and validating these constraints scans public.users and
-- takes the ordinary ALTER TABLE lock. Apply while the application and every
-- account writer are stopped.
-- Rewrite risk: none. Existing accounts receive one NULL window and the
-- constant default count zero; PostgreSQL does not rewrite their heap rows.

ALTER TABLE public.users
    ADD COLUMN publication_window_started_at timestamp with time zone,
    ADD COLUMN publication_count integer NOT NULL DEFAULT 0,
    ADD CONSTRAINT users_created_at_finite CHECK (
        pg_catalog.isfinite(created_at)
    ),
    ADD CONSTRAINT users_publication_window_consistent CHECK (
        (
            publication_window_started_at IS NULL
            AND publication_count = 0
        )
        OR
        (
            publication_window_started_at IS NOT NULL
            AND pg_catalog.isfinite(publication_window_started_at)
            AND publication_window_started_at >= created_at
            AND publication_count BETWEEN 1 AND 100000
        )
    );
