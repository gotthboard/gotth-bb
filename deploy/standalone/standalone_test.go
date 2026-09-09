package standalone

import (
	"os"
	"strings"
	"testing"
)

func TestStandaloneTopologyIsPinnedAndPrivate(t *testing.T) {
	t.Parallel()
	compose := readContractFile(t, "compose.yml")
	for _, required := range []string{
		"caddy:", "app:", "board-postgresql:", "authentik-postgresql:",
		"authentik-server:", "authentik-worker:",
		"caddy@sha256:5f5c8640aae01df9654968d946d8f1a56c497f1dd5c5cda4cf95ab7c14d58648",
		"ghcr.io/goauthentik/server@sha256:3ddf09bbf69ded6a9634ecd753a01608d477f811e99bb5ffe9fc2ef7ad1c6581",
		"postgres@sha256:a426e44bac0b759c95894d68e1a0ac03ecc20b619f498a91aae373bf06d8508d",
		"postgres@sha256:57c72fd2a128e416c7fcc499958864df5301e940bca0a56f58fddf30ffc07777",
		"127.0.0.1:${GOTTH_BB_AUTH_HTTP_PORT:-19000}:9000",
		"127.0.0.1:${GOTTH_BB_POSTGRES_PORT:-55435}:5432",
		`entrypoint: ["/bootstrap/entrypoint.sh"]`,
		"group_add:", `- "999"`, `- "65532"`,
		"/docker-entrypoint-initdb.d/10-gotth-bb-runtime.sh",
		"board_postgres_runtime_password",
		"network_mode: host", "internal: true", "create_host_path: false",
	} {
		if !strings.Contains(compose, required) {
			t.Errorf("compose.yml lacks %q", required)
		}
	}
	for _, forbidden := range []string{
		"/var/run/docker.sock", "container_name:", "latest", "POSTGRES_PASSWORD:",
		"AUTHENTIK_POSTGRESQL__PASSWORD:", "AUTHENTIK_SECRET_KEY:",
	} {
		if strings.Contains(compose, forbidden) {
			t.Errorf("compose.yml contains forbidden %q", forbidden)
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
		"admin {$GOTTH_BB_CADDY_ADMIN}", "{$GOTTH_BB_AUTH_HOST}",
		"reverse_proxy 127.0.0.1:{$GOTTH_BB_AUTH_HTTP_PORT}",
		"{$GOTTH_BB_BOARD_HOST}", "reverse_proxy 127.0.0.1:18082",
		"header_up X-Forwarded-For {remote_host}", "header_up -Forwarded", "header_up -X-Real-IP",
	} {
		if !strings.Contains(caddy, required) {
			t.Errorf("Caddyfile lacks %q", required)
		}
	}
	for _, required := range []string{
		"client_secret: !Env GOTTH_BB_OIDC_CLIENT_SECRET",
		"sub_mode: user_uuid", "url: !Env GOTTH_BB_OIDC_REDIRECT_URI",
		"meta_launch_url: !Env GOTTH_BB_PUBLIC_BASE_URL",
		"create_users_as_inactive: true", "group: !KeyOf access-group",
	} {
		if !strings.Contains(blueprint, required) {
			t.Errorf("Board blueprint lacks %q", required)
		}
	}
	if strings.Contains(blueprint, "hashed_user_id") {
		t.Fatal("Board blueprint couples subjects to the Authentik instance secret")
	}
	if !strings.Contains(apply, `os.environ.pop("GOTTH_BB_OIDC_CLIENT_SECRET", None)`) ||
		!strings.Contains(apply, `provider.client_secret != secret`) {
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
