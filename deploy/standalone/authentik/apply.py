from pathlib import Path
import json
import os

from authentik.blueprints.v1.importer import Importer
from authentik.core.models import Application, Group, Token, User
from authentik.flows.models import Flow, FlowStageBinding
from authentik.policies.models import PolicyBinding
from authentik.providers.oauth2.models import OAuth2Provider
from authentik.rbac.models import InitialPermissions, Role
from authentik.stages.invitation.models import Invitation
from guardian.models import RoleModelPermission, RoleObjectPermission


FLOW_IDENTITIES = {
    "open": ("gotth-bb-open", "8d29e230-3485-4ee6-a741-ec089e510001"),
    "approval": ("gotth-bb-approval", "8d29e230-3485-4ee6-a741-ec089e510002"),
    "invitation": ("gotth-bb-invitation", "8d29e230-3485-4ee6-a741-ec089e510003"),
}
GROUP_NAMES = {
    "accepted": "gotth-bb-users",
    "pending": "gotth-bb-pending",
    "suspended": "gotth-bb-suspended",
}


def read_secret(path, label):
    raw = Path(path).read_bytes()
    if not raw or len(raw) > 4096 or b"\x00" in raw or b"\n" in raw or b"\r" in raw:
        raise RuntimeError(f"{label} file has invalid framing")
    try:
        return raw.decode("utf-8", errors="strict")
    except UnicodeDecodeError as error:
        raise RuntimeError(f"{label} file is not UTF-8") from error


def permission_names(queryset):
    return set(
        queryset.values_list(
            "permission__content_type__app_label", "permission__codename"
        )
    )


oidc_secret = read_secret("/run/secrets/oidc_client_secret", "OIDC client secret")
control_secret = read_secret("/run/secrets/authentik_control_token", "control token")
os.environ["GOTTH_BB_OIDC_CLIENT_SECRET"] = oidc_secret
os.environ["GOTTH_BB_AUTHENTIK_CONTROL_TOKEN"] = control_secret
try:
    raw = Path("/bootstrap/board-blueprint.yaml").read_text(encoding="utf-8")
    importer = Importer.from_string(raw)
    valid, logs = importer.validate(raise_validation_errors=True)
    if not valid:
        raise RuntimeError(f"Board blueprint validation failed: {logs!r}")
    if not importer.apply():
        raise RuntimeError("Board blueprint apply failed")

    provider = OAuth2Provider.objects.get(name="GOTTH Board")
    application = Application.objects.get(slug="gotth-bb")
    role = Role.objects.get(name="gotth-bb-control")
    service = User.objects.get(username="gotth-bb-control")
    token = Token.objects.get(identifier="gotth-bb-control")
    initial = InitialPermissions.objects.get(name="gotth-bb-control-created-invitations")
    groups = {key: Group.objects.get(name=name) for key, name in GROUP_NAMES.items()}
    flows = {key: Flow.objects.get(slug=slug) for key, (slug, _) in FLOW_IDENTITIES.items()}

    if provider.client_secret != oidc_secret:
        raise RuntimeError("Board provider secret differs from mounted secret")
    if provider.client_id != "gotth-bb" or provider.sub_mode != "user_uuid":
        raise RuntimeError("Board provider identity contract differs")
    if application.provider_id != provider.pk:
        raise RuntimeError("Board application/provider binding differs")
    access = PolicyBinding.objects.get(target=application, group=groups["accepted"])
    if not access.enabled or access.negate or access.failure_result:
        raise RuntimeError("Board access-group binding differs")
    if service.type != "service_account" or not service.is_active:
        raise RuntimeError("Board control service account differs")
    if service.groups.exists() or set(service.roles.values_list("pk", flat=True)) != {role.pk}:
        raise RuntimeError("Board control service account membership differs")
    if token.user_id != service.pk or token.intent != "api" or token.expiring or token.key != control_secret:
        raise RuntimeError("Board control token differs")
    if Token.objects.including_expired().filter(user=service).exclude(pk=token.pk).exists():
        raise RuntimeError("Unexpected Board control service token exists")
    managed_role = service.get_managed_role()
    if managed_role is not None:
        if (
            managed_role.managed != managed_role.name
            or managed_role.users.exists()
            or managed_role.groups.exists()
        ):
            raise RuntimeError("Board control managed-role residue is unsafe")
        managed_role.delete()
    initial_permission_names = set(
        initial.permissions.values_list("content_type__app_label", "codename")
    )
    if initial.role_id != role.pk or initial_permission_names != {
        ("authentik_stages_invitation", "view_invitation"),
        ("authentik_stages_invitation", "delete_invitation"),
    }:
        raise RuntimeError("Board invitation initial permissions differ")

    model_permissions = permission_names(RoleModelPermission.objects.filter(role=role))
    if model_permissions != {
        ("authentik_core", "view_user"),
        ("authentik_stages_invitation", "add_invitation"),
    }:
        raise RuntimeError("Board control model permissions differ")
    object_permissions = RoleObjectPermission.objects.filter(role=role)
    service_invitations = list(
        Invitation.objects.filter(created_by=service).only("pk", "flow_id", "single_use")
    )
    if any(
        invitation.flow_id != flows["invitation"].pk or not invitation.single_use
        for invitation in service_invitations
    ):
        raise RuntimeError("Board control invitation scope differs")
    live_invitation_pks = {str(invitation.pk) for invitation in service_invitations}
    object_permissions.filter(
        permission__content_type__app_label="authentik_stages_invitation",
        permission__content_type__model="invitation",
    ).exclude(object_pk__in=live_invitation_pks).delete()
    expected_object_permissions = {
        ("authentik_core", codename, str(group.pk))
        for group in groups.values()
        for codename in ("view_group", "add_user_to_group", "remove_user_from_group")
    }
    expected_object_permissions.update(
        ("authentik_stages_invitation", codename, invitation_pk)
        for invitation_pk in live_invitation_pks
        for codename in ("view_invitation", "delete_invitation")
    )
    actual_object_permissions = {
        (app, codename, object_pk)
        for app, codename, object_pk in object_permissions.values_list(
            "permission__content_type__app_label", "permission__codename", "object_pk"
        )
    }
    if actual_object_permissions != expected_object_permissions:
        raise RuntimeError("Board control object permissions differ")

    for key, flow in flows.items():
        expected_slug, expected_uuid = FLOW_IDENTITIES[key]
        if flow.slug != expected_slug or str(flow.pk) != expected_uuid:
            raise RuntimeError("Board enrollment flow identity differs")
        bindings = FlowStageBinding.objects.filter(target=flow)
        guard_bindings = bindings.filter(stage__name="gotth-bb-registration-denied")
        expected_guard_count = 4 if key == "approval" else 3
        if guard_bindings.count() != expected_guard_count:
            raise RuntimeError("Board enrollment guard count differs")
        if guard_bindings.exclude(evaluate_on_plan=False, re_evaluate_policies=True).exists():
            raise RuntimeError("Board enrollment guard evaluation differs")
        for binding in guard_bindings:
            policies = PolicyBinding.objects.filter(target=binding)
            if policies.count() != 1 or policies.exclude(negate=False, failure_result=True).exists():
                raise RuntimeError("Board enrollment guard policy differs")
    if Flow.objects.filter(slug="gotth-bb-enrollment").exists():
        raise RuntimeError("Legacy unguarded enrollment flow remains")

    issuer_origin = os.environ["GOTTH_BB_OIDC_ISSUER_URL"].split("/application/", 1)[0]
    descriptor = {
        "version": 1,
        "issuer_origin": issuer_origin,
        "flows": {
            key: {"slug": flow.slug, "uuid": str(flow.pk)}
            for key, flow in sorted(flows.items())
        },
        "groups": {key: str(group.pk) for key, group in sorted(groups.items())},
    }
    print("AUTHENTIK_CONTROL_OBJECTS_JSON=" + json.dumps(descriptor, sort_keys=True, separators=(",", ":")))
    print("AUTHENTIK_BOARD_BLUEPRINT_APPLIED")
finally:
    os.environ.pop("GOTTH_BB_OIDC_CLIENT_SECRET", None)
    os.environ.pop("GOTTH_BB_AUTHENTIK_CONTROL_TOKEN", None)
    oidc_secret = ""
    control_secret = ""
