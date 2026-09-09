package standalone

import (
	"os"
	"strings"
	"testing"
)

func TestStandaloneTopologyIsPinnedAndPrivate(t *testing.T) {
	t.Parallel()
	compose := readContractFile(t, "compose.yml")
	commonStart := strings.Index(compose, "x-authentik-common:")
	servicesStart := strings.Index(compose, "\nservices:")
	if commonStart < 0 || servicesStart <= commonStart {
		t.Fatal("compose.yml lacks the Authentik common service boundary")
	}
	authentikCommon := compose[commonStart:servicesStart]
	for _, required := range []string{"cap_drop:\n    - ALL", "security_opt:\n    - no-new-privileges:true"} {
		if !strings.Contains(authentikCommon, required) {
			t.Errorf("Authentik common service contract lacks %q", required)
		}
	}
	for _, required := range []string{
		"caddy:", "app:", "board-postgresql:", "authentik-postgresql:",
		"authentik-server:", "authentik-worker:", "authentik-control-gateway:",
		"caddy@sha256:5f5c8640aae01df9654968d946d8f1a56c497f1dd5c5cda4cf95ab7c14d58648",
		"ghcr.io/goauthentik/server@sha256:3ddf09bbf69ded6a9634ecd753a01608d477f811e99bb5ffe9fc2ef7ad1c6581",
		"postgres@sha256:a426e44bac0b759c95894d68e1a0ac03ecc20b619f498a91aae373bf06d8508d",
		"postgres@sha256:57c72fd2a128e416c7fcc499958864df5301e940bca0a56f58fddf30ffc07777",
		"127.0.0.1:${GOTTH_BB_AUTH_HTTP_PORT:-19000}:9000",
		"127.0.0.1:${GOTTH_BB_POSTGRES_PORT:-55435}:5432",
		"LISTEN_ADDR: 127.0.0.1:${GOTTH_BB_APP_HTTP_PORT:-18082}",
		"GOTTH_BB_CADDY_UPSTREAM_SCHEME:",
		`entrypoint: ["/bootstrap/entrypoint.sh"]`,
		`user: "1000:1000"`,
		"group_add:", `- "999"`, `- "65532"`,
		"/docker-entrypoint-initdb.d/10-gotth-bb-runtime.sh",
		"board_postgres_runtime_password",
		"authentik_control_token", "AUTHENTIK_CONTROL_TOKEN_FILE:", "AUTHENTIK_CONTROL_OBJECTS_FILE:",
		"network_mode: host", "internal: true", "create_host_path: false",
	} {
		if !strings.Contains(compose, required) {
			t.Errorf("compose.yml lacks %q", required)
		}
	}
	for _, forbidden := range []string{
		"/var/run/docker.sock", "container_name:", "latest", "POSTGRES_PASSWORD:",
		"AUTHENTIK_POSTGRESQL__PASSWORD:", "AUTHENTIK_SECRET_KEY:",
		"user: root",
	} {
		if strings.Contains(compose, forbidden) {
			t.Errorf("compose.yml contains forbidden %q", forbidden)
		}
	}
}

func TestControlGatewayKeepsRawTokenOutOfBoard(t *testing.T) {
	t.Parallel()
	compose := readContractFile(t, "compose.yml")
	appStart := strings.Index(compose, "\n  app:")
	gatewayStart := strings.Index(compose, "\n  authentik-control-gateway:")
	boardDatabaseStart := strings.Index(compose, "\n  board-postgresql:")
	if appStart < 0 || gatewayStart <= appStart || boardDatabaseStart <= gatewayStart {
		t.Fatal("compose control service ordering is unavailable")
	}
	app := compose[appStart:gatewayStart]
	gateway := compose[gatewayStart:boardDatabaseStart]
	for _, required := range []string{
		"group_add:\n      - \"65531\"",
		"AUTHENTIK_CONTROL_SOCKET: /run/gotth-bb-control/authentik-control.sock",
		"INVITATION_FINGERPRINT_KEY_FILE: /run/secrets/invitation_fingerprint_key",
		"invitation_fingerprint_key",
		"condition: service_healthy",
	} {
		if !strings.Contains(app, required) {
			t.Errorf("app control boundary lacks %q", required)
		}
	}
	if strings.Contains(app, "authentik_control_token") || strings.Contains(app, "AUTHENTIK_CONTROL_TOKEN_FILE") {
		t.Fatal("Board app receives the raw Authentik control token")
	}
	for _, required := range []string{
		`user: "65533:65531"`, `- "65530"`,
		`command: ["/usr/local/bin/gotth-bb-authentik-gateway"]`,
		"AUTHENTIK_CONTROL_TOKEN_FILE: /run/secrets/authentik_control_token",
		"AUTHENTIK_CONTROL_SOCKET: /run/gotth-bb-control/authentik-control.sock",
		`test: ["CMD", "test", "-S", "/run/gotth-bb-control/authentik-control.sock"]`,
	} {
		if !strings.Contains(gateway, required) {
			t.Errorf("gateway boundary lacks %q", required)
		}
	}
	for _, forbidden := range []string{"database_runtime_url", "oidc_client_secret", "SMTP_", "DATABASE_URL"} {
		if strings.Contains(gateway, forbidden) {
			t.Errorf("gateway receives forbidden %q", forbidden)
		}
	}
}

func TestAuthentikEntrypointLoadsOnlyMountedSecrets(t *testing.T) {
	t.Parallel()
	entrypoint := readContractFile(t, "authentik/entrypoint.sh")
	for _, required := range []string{
		"AUTHENTIK_POSTGRESQL__PASSWORD=$(read_secret /run/secrets/authentik_postgres_password)",
		"AUTHENTIK_SECRET_KEY=$(read_secret /run/secrets/authentik_secret_key)",
		"exec dumb-init -- ak \"$1\"", "exec ak \"$@\"",
	} {
		if !strings.Contains(entrypoint, required) {
			t.Errorf("Authentik entrypoint lacks %q", required)
		}
	}
	if strings.Contains(entrypoint, "oidc_client_secret") {
		t.Fatal("Authentik common entrypoint can read the server-only OIDC secret")
	}
}

func TestStandaloneCaddyAndBlueprintContracts(t *testing.T) {
	t.Parallel()
	caddy := readContractFile(t, "Caddyfile")
	blueprint := readContractFile(t, "authentik/board-blueprint.yaml")
	apply := readContractFile(t, "authentik/apply.py")
	for _, required := range []string{
		"admin {$GOTTH_BB_CADDY_ADMIN}", "{$GOTTH_BB_AUTH_HOST}", "bind {$GOTTH_BB_CADDY_BIND}",
		"reverse_proxy 127.0.0.1:{$GOTTH_BB_AUTH_HTTP_PORT}",
		"{$GOTTH_BB_BOARD_HOST}", "reverse_proxy 127.0.0.1:{$GOTTH_BB_APP_HTTP_PORT}",
		"header_up X-Forwarded-For {$GOTTH_BB_CADDY_CLIENT_ADDRESS}", "header_up -Forwarded", "header_up -X-Real-IP",
		"header_up X-Forwarded-Proto {$GOTTH_BB_CADDY_UPSTREAM_SCHEME}",
	} {
		if !strings.Contains(caddy, required) {
			t.Errorf("Caddyfile lacks %q", required)
		}
	}
	if strings.Count(caddy, "header_up X-Forwarded-For {$GOTTH_BB_CADDY_CLIENT_ADDRESS}") != 2 ||
		strings.Count(caddy, "header_up X-Forwarded-Proto {$GOTTH_BB_CADDY_UPSTREAM_SCHEME}") != 2 ||
		strings.Count(caddy, "header_up -Forwarded") != 2 || strings.Count(caddy, "header_up -X-Real-IP") != 2 {
		t.Fatal("Caddy does not canonicalize client identity for both Board and Authentik")
	}
	for _, required := range []string{
		"client_secret: !Env GOTTH_BB_OIDC_CLIENT_SECRET",
		"sub_mode: user_uuid", "url: !Env GOTTH_BB_OIDC_REDIRECT_URI",
		"meta_launch_url: !Env GOTTH_BB_PUBLIC_BASE_URL",
		"create_users_as_inactive: true", "group: !KeyOf accepted-group",
		"gotth-bb-open", "gotth-bb-approval", "gotth-bb-invitation",
		"authentik_core.view_user", "authentik_stages_invitation.add_invitation",
		"evaluate_on_plan: false", "re_evaluate_policies: true",
		"allow_redirects=False", "timeout=2", "execution_logging: false",
		"/registration/admission/verified_email_open",
		"/registration/admission/administrator_approval",
		"/registration/admission/invitation_only",
		"continue_flow_without_invitation: false", "type: text_read_only",
	} {
		if !strings.Contains(blueprint, required) {
			t.Errorf("Board blueprint lacks %q", required)
		}
	}
	if strings.Contains(blueprint, "hashed_user_id") {
		t.Fatal("Board blueprint couples subjects to the Authentik instance secret")
	}
	if !strings.Contains(apply, `os.environ.pop("GOTTH_BB_OIDC_CLIENT_SECRET", None)`) ||
		!strings.Contains(apply, `os.environ.pop("GOTTH_BB_AUTHENTIK_CONTROL_TOKEN", None)`) ||
		!strings.Contains(apply, `provider.client_secret != oidc_secret`) ||
		!strings.Contains(apply, `b"\x00" in raw`) || !strings.Contains(apply, `b"\n" in raw`) {
		t.Fatal("blueprint apply does not clear and verify the mounted provider secret")
	}
}

func TestFreshDatabaseRoleInitializationDoesNotPutSecretInArguments(t *testing.T) {
	t.Parallel()
	init := readContractFile(t, "postgresql/init-runtime.sh")
	for _, required := range []string{
		`password_file=/run/secrets/board_postgres_runtime_password`,
		`\getenv runtime_password GOTTH_BB_RUNTIME_PASSWORD`,
		`CREATE ROLE gotth_bb_runtime LOGIN PASSWORD :'runtime_password';`,
		`unset GOTTH_BB_RUNTIME_PASSWORD`,
	} {
		if !strings.Contains(init, required) {
			t.Errorf("runtime-role initializer lacks %q", required)
		}
	}
	if strings.Contains(init, `--set=runtime_password`) {
		t.Fatal("runtime-role initializer places the password in process arguments")
	}
}

func TestStandalonePreflightRejectsDriftBeforeCompose(t *testing.T) {
	t.Parallel()
	preflight := readContractFile(t, "preflight.sh")
	for _, required := range []string{
		`[ "$(id -u)" -eq 0 ]`,
		`deployment.env must be root:root mode 0600`,
		`Caddy bind must be 0.0.0.0 or 127.0.0.1`,
		`direct Board Caddy address differs from its public origin`,
		`edge-fed $label Caddy address differs from its public origin`,
		`edge-fed Caddy must consume the edge canonical client identity`,
		`edge-fed Caddy must report the public HTTPS scheme upstream`,
		`OIDC redirect URI differs from the Board callback`,
		`Board and Authentik public origins must differ`,
		`loopback service ports overlap`,
		`Board image identity differs from the release package`,
		`durable and secret paths overlap`,
		`case "$canonical/" in "$previous/"*`,
		`contains NUL, CR, or LF framing`,
		`app must not receive an Authentik control token path`,
		`invitation fingerprint key must contain exactly 32 bytes`,
		`65533:65531:750 directory`,
		`app OIDC issuer differs from dedicated Authentik`,
		`docker compose --env-file "$deployment_env"`,
		`echo STANDALONE_PREFLIGHT_OK`,
	} {
		if !strings.Contains(preflight, required) {
			t.Errorf("preflight.sh lacks %q", required)
		}
	}
}

func TestAuthentikApplyUsesOneExplicitComposeProject(t *testing.T) {
	t.Parallel()
	apply := readContractFile(t, "apply-authentik.sh")
	for _, required := range []string{
		`project=${1:-gotth-bb-standalone}`,
		`--project-name "$project"`,
		`invalid Compose project name`,
	} {
		if !strings.Contains(apply, required) {
			t.Errorf("apply-authentik.sh lacks %q", required)
		}
	}
}

func readContractFile(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if len(contents) == 0 || contents[len(contents)-1] != '\n' || strings.IndexByte(string(contents), 0) >= 0 {
		t.Fatalf("%s has invalid text framing", path)
	}
	return string(contents)
}
