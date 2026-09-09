"""Disposable-tenant proof of B1-09 raw-token authority before gateway containment."""

from datetime import timedelta
from pathlib import Path

import requests
from django.utils.timezone import now

from authentik.core.models import Group, User
from authentik.flows.models import Flow
from authentik.stages.invitation.models import Invitation


ORIGIN = "http://127.0.0.1:9000/api/v3"
CONTROL_TOKEN = Path("/run/secrets/authentik_control_token").read_text(encoding="utf-8")
HEADERS = {"Authorization": f"Bearer {CONTROL_TOKEN}", "Accept": "application/json"}
FIXTURE = "gotth-bb-b109-permission-fixture"


def call(method, path, expected, body=None):
    response = requests.request(
        method,
        ORIGIN + path,
        headers=HEADERS,
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

    # Exact Board-group object permissions and atomic membership actions.
    for group_name in ("gotth-bb-users", "gotth-bb-pending", "gotth-bb-suspended"):
        group = Group.objects.get(name=group_name)
        call("GET", f"/core/groups/{group.pk}/?include_users=false", {200})
        call("POST", f"/core/groups/{group.pk}/add_user/", {204}, {"pk": test_user.pk})
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
    forbidden("POST", "/core/tokens/", {})
    forbidden("GET", "/tasks/tasks/")
    forbidden("GET", "/events/events/export/")

    call("DELETE", f"/stages/invitation/invitations/{created_uuid}/", {204})
    created_uuid = None
    print("AUTHENTIK_RAW_PERMISSION_MATRIX_WITH_DOCUMENTED_EMAIL_EXCESS_OK")
finally:
    if created_uuid:
        Invitation.objects.filter(pk=created_uuid).delete()
    Invitation.objects.filter(pk=foreign_invitation.pk).delete()
    outsider_group.delete()
    test_user.delete()
    CONTROL_TOKEN = ""
