-- Lock risk: replacing the closed action-type constraint takes a brief
-- ACCESS EXCLUSIVE lock on moderation_actions and scans that table once.
-- Rewrite risk: none. The identity row is changed only by the explicit,
-- stopped-application operator command after this migration commits.

ALTER TABLE public.moderation_actions
    DROP CONSTRAINT moderation_actions_action_type_closed,
    ADD CONSTRAINT moderation_actions_action_type_closed CHECK (
        action_type IN (
            'bootstrap_administrator', 'rebind_external_identity', 'change_role',
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
    );
