"""Disposable-tenant proof of B1-09 raw-token authority before gateway containment."""

from datetime import timedelta
from pathlib import Path

import requests
from django.utils.timezone import now

from authentik.core.models import Group, Token, User
from authentik.flows.models import Flow
from authentik.rbac.models import InitialPermissions
from authentik.stages.invitation.models import Invitation


ORIGIN = "http://127.0.0.1:9000/api/v3"
CONTROL_TOKEN = Path("/run/secrets/authentik_control_token").read_text(encoding="utf-8")
HEADERS = {"Authorization": f"Bearer {CONTROL_TOKEN}", "Accept": "application/json"}
FIXTURE = "gotth-bb-b109-permission-fixture"
CHILD_TOKEN = FIXTURE + "-child-token"


def call(method, path, expected, body=None, request_headers=None):
    response = requests.request(
        method,
        ORIGIN + path,
        headers=HEADERS if request_headers is None else request_headers,
        json=body,
        timeout=2,
        allow_redirects=False,
    )
    if response.status_code not in expected:
        raise RuntimeError(f"permission matrix {method} {path} returned {response.status_code}")
    return response


def forbidden(method, path, body=None):
    return call(method, path, {403, 404}, body)


test_user = User.objects.create(
    username=FIXTURE,
    name="Permission fixture",
    email="permission-fixture@example.invalid",
    type="external",
    path="users/gotth-bb",
)
test_user.set_unusable_password()
test_user.save()
outsider_group = Group.objects.create(name=FIXTURE, is_superuser=False)
flow = Flow.objects.get(slug="gotth-bb-invitation")
foreign_invitation = Invitation.objects.create(
    name=FIXTURE + "-foreign",
    flow=flow,
    single_use=True,
    created_by=test_user,
    fixed_data={"email": "foreign@example.invalid"},
    expires=now() + timedelta(hours=1),
)
created_uuid = None
try:
    # The two admitted model-level capabilities.
    call("GET", f"/core/users/?uuid={test_user.uuid}&page_size=2", {200})
    invitation_response = call(
        "POST",
        "/stages/invitation/invitations/",
        {201},
        {
            "name": FIXTURE + "-owned",
            "flow": str(flow.pk),
            "single_use": True,
            "fixed_data": {"email": "owned@example.invalid"},
            "expires": (now() + timedelta(hours=1)).isoformat(),
        },
    )
    created_uuid = invitation_response.json()["pk"]
    invitation_page = call(
        "GET",
        "/stages/invitation/invitations/"
        "?flow__slug=gotth-bb-invitation&page_size=51",
        {200},
    ).json()
    if invitation_page["pagination"]["next"] != 0 or not any(
        item.get("pk") == created_uuid for item in invitation_page["results"]
    ):
        raise RuntimeError("pinned invitation list omitted creator-owned fixture")

    # Exact Board-group object permissions and atomic membership actions.
    for group_name in ("gotth-bb-users", "gotth-bb-pending", "gotth-bb-suspended"):
        group = Group.objects.get(name=group_name)
        call("GET", f"/core/groups/{group.pk}/?include_users=false", {200})
        call("POST", f"/core/groups/{group.pk}/add_user/", {204}, {"pk": test_user.pk})
        if group_name == "gotth-bb-pending":
            pending_page = call(
                "GET",
                f"/core/users/?groups_by_pk={group.pk}"
                "&include_groups=false&include_roles=false&page_size=51",
                {200},
            ).json()
            if pending_page["pagination"]["next"] != 0 or not any(
                item.get("uuid") == str(test_user.uuid)
                for item in pending_page["results"]
            ):
                raise RuntimeError("pinned pending-group query omitted fixture")
        call("POST", f"/core/groups/{group.pk}/remove_user/", {204}, {"pk": test_user.pk})

    # Creator-scoped initial permissions apply only to the invitation just created.
    call("GET", f"/stages/invitation/invitations/{created_uuid}/", {200})
    forbidden("GET", f"/stages/invitation/invitations/{foreign_invitation.pk}/")
    forbidden("DELETE", f"/stages/invitation/invitations/{foreign_invitation.pk}/")
    # Authentik 2026.5.2 incorrectly inherits this action from view_invitation.
    # The gateway must contain this proven excess; the raw-token matrix must not
    # pretend Authentik denies it.
    call(
        "POST",
        f"/stages/invitation/invitations/{created_uuid}/send_email/",
        {204},
        {"email_addresses": ["nobody@example.invalid"]},
    )
    forbidden(
        "POST",
        f"/stages/invitation/invitations/{foreign_invitation.pk}/send_email/",
        {"email_addresses": ["nobody@example.invalid"]},
    )

    # Every unadmitted administrative capability remains denied.
    forbidden("GET", "/admin/system/")
    forbidden("POST", "/core/users/", {"username": "forbidden"})
    forbidden("PATCH", f"/core/users/{test_user.pk}/", {"name": "forbidden"})
    forbidden("DELETE", f"/core/users/{test_user.pk}/")
    forbidden("POST", f"/core/users/{test_user.pk}/set_password/", {"password": "forbidden"})
    forbidden("POST", f"/core/users/{test_user.pk}/recovery/", {})
    forbidden("POST", f"/core/users/{test_user.pk}/impersonate/", {})
    forbidden("POST", "/core/groups/", {"name": "forbidden"})
    forbidden("PATCH", f"/core/groups/{outsider_group.pk}/", {"name": "forbidden"})
    forbidden("DELETE", f"/core/groups/{outsider_group.pk}/")
    forbidden("POST", f"/core/groups/{outsider_group.pk}/add_user/", {"pk": test_user.pk})
    forbidden("POST", "/flows/instances/", {})
    forbidden("POST", "/stages/deny/", {})
    forbidden("POST", "/policies/expression/", {})
    forbidden("POST", "/core/applications/", {})
    forbidden("POST", "/providers/oauth2/", {})
    forbidden("POST", "/rbac/roles/", {})
    # Authentik deliberately permits every authenticated non-superuser to
    # issue another API token for itself (`rbac_allow_create_without_perm`).
    # This is a second raw-token excess that only gateway isolation contains.
    call(
        "POST",
        "/core/tokens/",
        {201},
        {
            "identifier": CHILD_TOKEN,
            "intent": "api",
            "description": "disposable B1-09 permission fixture",
        },
    )
    child_token = Token.objects.get(identifier=CHILD_TOKEN)
    control_user = User.objects.get(username="gotth-bb-control")
    if child_token.user_id != control_user.pk or child_token.user_id == test_user.pk:
        raise RuntimeError("self-issued token was not forced to control service account")
    child_key = call("GET", f"/core/tokens/{CHILD_TOKEN}/view_key/", {200}).json()["key"]
    if not child_key or len(child_key) > 4096 or any(char in child_key for char in "\r\n\x00"):
        raise RuntimeError("self-issued token key has invalid framing")
    call(
        "GET",
        f"/core/users/?uuid={control_user.uuid}&page_size=2",
        {200},
        request_headers={
            "Authorization": f"Bearer {child_key}",
            "Accept": "application/json",
        },
    )
    child_key = ""
    changed_token = call(
        "PATCH",
        f"/core/tokens/{CHILD_TOKEN}/",
        {200},
        {"description": "forbidden mutation"},
    ).json()
    if changed_token.get("description") != "forbidden mutation":
        raise RuntimeError("self-issued token update did not persist")
    call("DELETE", f"/core/tokens/{CHILD_TOKEN}/", {204})
    forbidden("GET", "/tasks/tasks/")
    forbidden("GET", "/events/events/export/")

    call("DELETE", f"/stages/invitation/invitations/{created_uuid}/", {204})
    created_uuid = None

    # Prove permission subtraction cannot preserve reconciliation. With only
    # delete_invitation assigned at create time, the object is absent from the
    # list and retrieve/send/delete all fail because object resolution itself
    # requires view permission.
    initial = InitialPermissions.objects.get(
        name="gotth-bb-control-created-invitations"
    )
    original_permissions = list(initial.permissions.all())
    delete_permission = next(
        permission
        for permission in original_permissions
        if permission.codename == "delete_invitation"
    )
    delete_only_uuid = None
    initial.permissions.set([delete_permission])
    try:
        delete_only_response = call(
            "POST",
            "/stages/invitation/invitations/",
            {201},
            {
                "name": FIXTURE + "-delete-only",
                "flow": str(flow.pk),
                "single_use": True,
                "fixed_data": {"email": "delete-only@example.invalid"},
                "expires": (now() + timedelta(hours=1)).isoformat(),
            },
        )
        delete_only_uuid = delete_only_response.json()["pk"]
        forbidden("GET", "/stages/invitation/invitations/?page_size=100")
        forbidden("GET", f"/stages/invitation/invitations/{delete_only_uuid}/")
        forbidden(
            "POST",
            f"/stages/invitation/invitations/{delete_only_uuid}/send_email/",
            {"email_addresses": ["nobody@example.invalid"]},
        )
        forbidden("DELETE", f"/stages/invitation/invitations/{delete_only_uuid}/")
    finally:
        initial.permissions.set(original_permissions)
        if delete_only_uuid:
            Invitation.objects.filter(pk=delete_only_uuid).delete()

    print("AUTHENTIK_RAW_PERMISSION_MATRIX_WITH_DOCUMENTED_EXCESSES_OK")
finally:
    if created_uuid:
        Invitation.objects.filter(pk=created_uuid).delete()
    Invitation.objects.filter(pk=foreign_invitation.pk).delete()
    outsider_group.delete()
    test_user.delete()
    Token.objects.filter(identifier=CHILD_TOKEN).delete()
    CONTROL_TOKEN = ""
