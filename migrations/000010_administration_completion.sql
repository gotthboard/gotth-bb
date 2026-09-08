-- Lock risk: adding and validating revision checks scans users, forum_groups,
-- and areas; replacing audit checks takes an ACCESS EXCLUSIVE lock on
-- moderation_actions. Run this ordinary atomic migration while the application
-- and every writer are stopped.
-- Rewrite risk: none. Existing rows receive the constant default revision 1;
-- no content or account row is rewritten or backfilled by application code.

CREATE TABLE public.site_settings (
    singleton boolean PRIMARY KEY DEFAULT true,
    site_name text NOT NULL,
    site_description text NOT NULL,
    brand_theme text NOT NULL,
    rules_markdown text NOT NULL,
    rules_html text NOT NULL,
    rules_renderer_version text NOT NULL,
    administration_revision bigint NOT NULL DEFAULT 1,
    updated_at timestamp with time zone NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT site_settings_singleton_true CHECK (singleton),
    CONSTRAINT site_settings_name_shape CHECK (
        char_length(site_name) >= 1
        AND char_length(site_name) <= 80
        AND site_name !~ '[[:cntrl:]]'
    ),
    CONSTRAINT site_settings_description_shape CHECK (
        char_length(site_description) <= 280
        AND site_description !~ '[[:cntrl:]]'
    ),
    CONSTRAINT site_settings_theme_closed CHECK (
        brand_theme IN ('blue', 'cyan', 'emerald', 'amber', 'rose')
    ),
    CONSTRAINT site_settings_rules_source_size CHECK (
        octet_length(rules_markdown) <= 65536
    ),
    CONSTRAINT site_settings_rules_html_size CHECK (
        octet_length(rules_html) <= 262144
    ),
    CONSTRAINT site_settings_rules_tuple_current CHECK (
        rules_renderer_version = 'goldmark-v1.8.5-gfm-bluemonday-v1.0.27-p2'
        AND ((rules_markdown = '' AND rules_html = '')
             OR (rules_markdown <> '' AND rules_html <> ''))
    ),
    CONSTRAINT site_settings_revision_positive CHECK (
        administration_revision > 0
    ),
    CONSTRAINT site_settings_updated_at_finite CHECK (
        pg_catalog.isfinite(updated_at)
    )
);

INSERT INTO public.site_settings (
    singleton,
    site_name,
    site_description,
    brand_theme,
    rules_markdown,
    rules_html,
    rules_renderer_version,
    administration_revision
) VALUES (
    true,
    'GOTTH Board',
    'Community discussions, plainly organized.',
    'blue',
    '',
    '',
    'goldmark-v1.8.5-gfm-bluemonday-v1.0.27-p2',
    1
);

ALTER TABLE public.users
    ADD COLUMN administration_revision bigint NOT NULL DEFAULT 1,
    ADD CONSTRAINT users_administration_revision_positive CHECK (
        administration_revision > 0
    );

ALTER TABLE public.forum_groups
    ADD COLUMN administration_revision bigint NOT NULL DEFAULT 1,
    ADD CONSTRAINT forum_groups_administration_revision_positive CHECK (
        administration_revision > 0
    );

ALTER TABLE public.areas
    ADD COLUMN administration_revision bigint NOT NULL DEFAULT 1,
    ADD CONSTRAINT areas_administration_revision_positive CHECK (
        administration_revision > 0
    );

ALTER TABLE public.moderation_actions
    ADD COLUMN target_site boolean,
    ADD CONSTRAINT moderation_actions_target_site_true CHECK (
        target_site IS NULL OR target_site
    ),
    DROP CONSTRAINT moderation_actions_target_type_closed,
    ADD CONSTRAINT moderation_actions_target_type_closed CHECK (
        target_type IN ('user', 'group', 'area', 'topic', 'post', 'report', 'site')
    ),
    DROP CONSTRAINT moderation_actions_target_consistent,
    ADD CONSTRAINT moderation_actions_target_consistent CHECK (
        num_nonnulls(
            target_user_id,
            target_group_id,
            target_area_id,
            target_topic_id,
            target_post_id,
            target_report_id,
            target_site
        ) = 1
        AND (target_type <> 'user' OR target_user_id IS NOT NULL)
        AND (target_type <> 'group' OR target_group_id IS NOT NULL)
        AND (target_type <> 'area' OR target_area_id IS NOT NULL)
        AND (target_type <> 'topic' OR target_topic_id IS NOT NULL)
        AND (target_type <> 'post' OR target_post_id IS NOT NULL)
        AND (target_type <> 'report' OR target_report_id IS NOT NULL)
        AND (target_type <> 'site' OR target_site IS NOT NULL)
    ),
    DROP CONSTRAINT moderation_actions_action_type_closed,
    ADD CONSTRAINT moderation_actions_action_type_closed CHECK (
        action_type IN (
            'bootstrap_administrator', 'change_role',
            'grant_group_membership', 'revoke_group_membership',
            'create_group', 'rename_group',
            'create_area', 'update_area', 'change_area_visibility', 'change_area_posting_mode',
            'grant_area_group', 'revoke_area_group',
            'lock_topic', 'unlock_topic', 'hide_topic', 'restore_topic',
            'pin_topic', 'unpin_topic', 'move_topic', 'archive_topic',
            'hide_post', 'restore_post', 'redact_post',
            'warn_user', 'mute_user', 'suspend_user', 'reinstate_user',
            'assign_report', 'note_report', 'resolve_report', 'dismiss_report',
            'update_site_settings'
        )
    ),
    DROP CONSTRAINT moderation_actions_reason_length,
    ADD CONSTRAINT moderation_actions_reason_length CHECK (
        reason IS NULL
        OR octet_length(reason) >= 1
        AND octet_length(reason) <= 2000
        AND reason !~ '[[:cntrl:]]'
        AND reason = pg_catalog.btrim(reason, ' ')
    ),
    DROP CONSTRAINT moderation_actions_reason_required,
    ADD CONSTRAINT moderation_actions_reason_required CHECK (
        action_type NOT IN (
            'change_role',
            'grant_group_membership', 'revoke_group_membership',
            'create_group', 'rename_group',
            'create_area', 'update_area',
            'grant_area_group', 'revoke_area_group',
            'move_topic', 'hide_topic', 'archive_topic', 'hide_post', 'redact_post',
            'warn_user', 'mute_user', 'suspend_user', 'reinstate_user',
            'resolve_report', 'dismiss_report',
            'update_site_settings'
        )
        OR reason IS NOT NULL
    );
