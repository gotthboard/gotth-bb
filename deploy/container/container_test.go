package container

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEntrypointLoadsSecretsAndReplacesItself(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	databaseFile := filepath.Join(directory, "database-url")
	oidcFile := filepath.Join(directory, "oidc-secret")
	if err := os.WriteFile(databaseFile, []byte("postgres://runtime:fake@postgresql:5432/gotth_bb\n"), 0o600); err != nil {
		t.Fatalf("write database fixture: %v", err)
	}
	if err := os.WriteFile(oidcFile, []byte("fake-oidc-secret\n"), 0o600); err != nil {
		t.Fatalf("write OIDC fixture: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "/bin/sh", "entrypoint.sh", "/bin/sh", "-c", `printf '%s|%s|%s|%s' "$DATABASE_URL" "$OIDC_CLIENT_SECRET" "${DATABASE_URL_FILE-unset}" "${OIDC_CLIENT_SECRET_FILE-unset}"`)
	command.Env = append(os.Environ(), "DATABASE_URL_FILE="+databaseFile, "OIDC_CLIENT_SECRET_FILE="+oidcFile)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("entrypoint success error = %v, output = %q", err, output)
	}
	want := "postgres://runtime:fake@postgresql:5432/gotth_bb|fake-oidc-secret|unset|unset"
	if string(output) != want {
		t.Fatalf("entrypoint output = %q, want %q", output, want)
	}
}

func TestEntrypointFailsClosedWithoutSecretOrCommand(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		args []string
		env  []string
	}{
		{name: "missing database secret", args: []string{"/bin/true"}, env: []string{"DATABASE_URL_FILE=/does/not/exist"}},
		{name: "missing command"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, "/bin/sh", append([]string{"entrypoint.sh"}, test.args...)...)
			command.Env = append(os.Environ(), test.env...)
			output, err := command.CombinedOutput()
			if err == nil {
				t.Fatalf("entrypoint returned success, output = %q", output)
			}
			if strings.Contains(string(output), "fake-oidc-secret") || strings.Contains(string(output), "postgres://") {
				t.Fatalf("entrypoint exposed secret material: %q", output)
			}
		})
	}
}

func TestContainerAndComposeContractsRemainHardened(t *testing.T) {
	t.Parallel()

	containerfile, err := os.ReadFile("Containerfile")
	if err != nil {
		t.Fatalf("read Containerfile: %v", err)
	}
	compose, err := os.ReadFile("compose.yml")
	if err != nil {
		t.Fatalf("read compose.yml: %v", err)
	}
	for _, required := range []string{
		"FROM alpine@sha256:25109184c71bdad752c8312a8623239686a9a2071e8825f20acb8f2198c3f659",
		"USER 65532:65532",
		"COPY --chmod=0755 gotth-bb-authentik-gateway /usr/local/bin/gotth-bb-authentik-gateway",
		"HEALTHCHECK",
		`ENTRYPOINT ["/usr/local/bin/gotth-bb-entrypoint"]`,
	} {
		if !strings.Contains(string(containerfile), required) {
			t.Errorf("Containerfile lacks %q", required)
		}
	}
	for _, required := range []string{
		"app:",
		"postgresql:",
		"LISTEN_ADDR: 127.0.0.1:18082",
		"ACTIVITY_CURSOR_KEYRING_FILE: /run/secrets/activity_cursor_keyring",
		"GOTTH_BB_ACTIVITY_CURSOR_KEYRING_FILE",
		"ABUSE_RULES_FILE: /run/config/gotth-bb-abuse-rules",
		"GOTTH_BB_ABUSE_RULES_FILE",
		"create_host_path: false",
		"network_mode: host",
		"read_only: true",
		"cap_drop:",
		"no-new-privileges:true",
		"driver: journald",
		"condition: service_healthy",
		"http://127.0.0.1:18082/health/live",
		"/tank/gotth-bb/postgres17:/var/lib/postgresql/data",
	} {
		if !strings.Contains(string(compose), required) {
			t.Errorf("compose.yml lacks %q", required)
		}
	}
	for _, forbidden := range []string{"POSTGRES_PASSWORD:", "DATABASE_URL:", "OIDC_CLIENT_SECRET:"} {
		if strings.Contains(string(compose), forbidden) {
			t.Errorf("compose.yml contains secret-bearing key %q", forbidden)
		}
	}
	if strings.Contains(string(compose), "127.0.0.1:18082:8080") {
		t.Error("compose.yml retains the rejected bridge-port mapping")
	}
}

func TestImageBuilderStreamsExactPackageAndVerifiesExecutables(t *testing.T) {
	t.Parallel()
	command := exec.Command("/bin/sh", "-n", "build-image.sh")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("sh -n build-image.sh: %v\n%s", err, output)
	}
	body, err := os.ReadFile("build-image.sh")
	if err != nil {
		t.Fatalf("read build-image.sh: %v", err)
	}
	text := string(body)
	for _, required := range []string{
		`tar -C "$package_root" -cf "$context" .`,
		`docker build --no-cache --network=none`,
		`package_digest=$(sha256sum`,
		`image_digest=$(docker run --rm --entrypoint sha256sum`,
		`gotth-bb-migrate gotth-bb-operator gotth-bb-authentik-gateway`,
		`image labels differ from RELEASE.txt`,
		`image reference already exists`,
	} {
		if !strings.Contains(text, required) {
			t.Errorf("build-image.sh lacks %q", required)
		}
	}
}

func TestImageBuilderCommitIdentityBoundary(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	containerDirectory := filepath.Join(root, "deploy", "container")
	if err := os.MkdirAll(containerDirectory, 0o755); err != nil {
		t.Fatalf("create container fixture: %v", err)
	}
	script, err := os.ReadFile("build-image.sh")
	if err != nil {
		t.Fatalf("read build-image.sh: %v", err)
	}
	scriptPath := filepath.Join(containerDirectory, "build-image.sh")
	if err := os.WriteFile(scriptPath, script, 0o755); err != nil {
		t.Fatalf("write build-image.sh: %v", err)
	}
	for _, binary := range []string{"gotth-bb", "gotth-bb-migrate", "gotth-bb-operator", "gotth-bb-authentik-gateway"} {
		if err := os.WriteFile(filepath.Join(root, binary), []byte("fixture\n"), 0o755); err != nil {
			t.Fatalf("write %s: %v", binary, err)
		}
	}
	bin := filepath.Join(root, "test-bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatalf("create test bin: %v", err)
	}
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fake docker: %v", err)
	}
	run := func(commit string) string {
		t.Helper()
		release := "version=1.0.0-beta.1.3\ncommit=" + commit + "\n"
		if err := os.WriteFile(filepath.Join(root, "RELEASE.txt"), []byte(release), 0o644); err != nil {
			t.Fatalf("write RELEASE.txt: %v", err)
		}
		command := exec.Command("/bin/sh", scriptPath, "gotth-bb:test")
		command.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
		output, _ := command.CombinedOutput()
		return string(output)
	}
	if output := run(strings.Repeat("a", 40)); !strings.Contains(output, "image reference already exists") {
		t.Fatalf("valid 40-character commit did not reach the next boundary: %q", output)
	}
	for _, invalid := range []string{strings.Repeat("a", 39), strings.Repeat("a", 41), strings.Repeat("a", 39) + "G"} {
		if output := run(invalid); !strings.Contains(output, "release commit is invalid") {
			t.Fatalf("invalid commit %q output = %q", invalid, output)
		}
	}
}
