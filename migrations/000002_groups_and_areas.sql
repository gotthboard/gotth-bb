-- Lock risk: creates new relations, indexes, and row-local integrity triggers.
-- Rewrite risk: none on a fresh database. Area policy triggers lock one area row.

CREATE TABLE public.forum_groups (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name text NOT NULL,
    created_by bigint NOT NULL REFERENCES public.users (id) ON DELETE RESTRICT,
    created_at timestamp with time zone NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamp with time zone NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT forum_groups_name_length CHECK (char_length(name) BETWEEN 1 AND 80),
    CONSTRAINT forum_groups_timestamps_ordered CHECK (updated_at >= created_at)
);

CREATE UNIQUE INDEX forum_groups_name_lower_unique
    ON public.forum_groups (lower(name));

CREATE TABLE public.forum_group_members (
    group_id bigint NOT NULL REFERENCES public.forum_groups (id) ON DELETE CASCADE,
    user_id bigint NOT NULL REFERENCES public.users (id) ON DELETE CASCADE,
    granted_by bigint NOT NULL REFERENCES public.users (id) ON DELETE RESTRICT,
    created_at timestamp with time zone NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (group_id, user_id)
);

CREATE INDEX forum_group_members_user_group_idx
    ON public.forum_group_members (user_id, group_id);

CREATE TABLE public.areas (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    slug text NOT NULL,
    name text NOT NULL,
    description text NOT NULL DEFAULT '',
    display_order integer NOT NULL DEFAULT 0,
    visibility text NOT NULL DEFAULT 'public',
    posting_mode text NOT NULL DEFAULT 'normal',
    created_by bigint NOT NULL REFERENCES public.users (id) ON DELETE RESTRICT,
    updated_by bigint NOT NULL REFERENCES public.users (id) ON DELETE RESTRICT,
    created_at timestamp with time zone NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamp with time zone NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT areas_slug_unique UNIQUE (slug),
    CONSTRAINT areas_slug_format CHECK (
        char_length(slug) BETWEEN 1 AND 80
        AND slug ~ '^[a-z0-9]+(-[a-z0-9]+)*$'
    ),
    CONSTRAINT areas_name_length CHECK (char_length(name) BETWEEN 1 AND 120),
    CONSTRAINT areas_description_length CHECK (char_length(description) <= 4000),
    CONSTRAINT areas_display_order_nonnegative CHECK (display_order >= 0),
    CONSTRAINT areas_visibility_closed CHECK (visibility IN ('public', 'authenticated', 'groups')),
    CONSTRAINT areas_posting_mode_closed CHECK (posting_mode IN ('normal', 'read_only', 'archived')),
    CONSTRAINT areas_timestamps_ordered CHECK (updated_at >= created_at)
);

CREATE INDEX areas_display_idx
    ON public.areas (display_order, id);

CREATE TABLE public.area_groups (
    area_id bigint NOT NULL REFERENCES public.areas (id) ON DELETE CASCADE,
    group_id bigint NOT NULL REFERENCES public.forum_groups (id) ON DELETE CASCADE,
    added_by bigint NOT NULL REFERENCES public.users (id) ON DELETE RESTRICT,
    created_at timestamp with time zone NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (area_id, group_id)
);

CREATE INDEX area_groups_group_area_idx
    ON public.area_groups (group_id, area_id);

CREATE FUNCTION public.gotth_reject_area_slug_change()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public
AS $$
BEGIN
    IF NEW.slug IS DISTINCT FROM OLD.slug THEN
        RAISE EXCEPTION 'published area slug is immutable' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER areas_slug_immutable
BEFORE UPDATE OF slug ON public.areas
FOR EACH ROW
EXECUTE FUNCTION public.gotth_reject_area_slug_change();

CREATE FUNCTION public.gotth_require_group_visible_area()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public
AS $$
DECLARE
    area_visibility text;
BEGIN
    SELECT visibility INTO area_visibility
    FROM public.areas
    WHERE id = NEW.area_id
    FOR UPDATE;

    IF area_visibility IS NULL OR area_visibility <> 'groups' THEN
        RAISE EXCEPTION 'area group mapping requires groups visibility' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER area_groups_require_group_visibility
BEFORE INSERT OR UPDATE OF area_id ON public.area_groups
FOR EACH ROW
EXECUTE FUNCTION public.gotth_require_group_visible_area();

CREATE FUNCTION public.gotth_reject_visibility_with_group_mappings()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public
AS $$
BEGIN
    IF NEW.visibility <> 'groups'
       AND OLD.visibility = 'groups'
       AND EXISTS (SELECT 1 FROM public.area_groups WHERE area_id = OLD.id) THEN
        RAISE EXCEPTION 'remove area group mappings before changing visibility' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER areas_visibility_preserves_group_mappings
BEFORE UPDATE OF visibility ON public.areas
FOR EACH ROW
EXECUTE FUNCTION public.gotth_reject_visibility_with_group_mappings();
