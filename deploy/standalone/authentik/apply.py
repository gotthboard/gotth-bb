from pathlib import Path
import os

from authentik.blueprints.v1.importer import Importer
from authentik.core.models import Application, Group
from authentik.policies.models import PolicyBinding
from authentik.providers.oauth2.models import OAuth2Provider


def read_secret(path):
    value = Path(path).read_text(encoding="utf-8").rstrip("\n")
    if not value or "\n" in value or "\r" in value:
        raise RuntimeError("OIDC client secret file has invalid framing")
    return value


secret = read_secret("/run/secrets/oidc_client_secret")
os.environ["GOTTH_BB_OIDC_CLIENT_SECRET"] = secret
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
    group = Group.objects.get(name="gotth-bb-users")
    binding = PolicyBinding.objects.get(target=application, group=group)
    if provider.client_secret != secret:
        raise RuntimeError("Board provider secret differs from mounted secret")
    if provider.client_id != "gotth-bb" or provider.sub_mode != "user_uuid":
        raise RuntimeError("Board provider identity contract differs")
    if application.provider_id != provider.pk:
        raise RuntimeError("Board application/provider binding differs")
    if not binding.enabled or binding.negate or binding.failure_result:
        raise RuntimeError("Board access-group binding differs")
    print("AUTHENTIK_BOARD_BLUEPRINT_APPLIED")
finally:
    os.environ.pop("GOTTH_BB_OIDC_CLIENT_SECRET", None)
    secret = ""
