// Package releaseartifact builds one deterministic, checksummed native release
// archive from an exact clean repository commit.
package releaseartifact

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/gotthboard/gotth-bb/internal/buildinfo"
)

const (
	linkerPackage          = "github.com/gotthboard/gotth-bb/internal/buildinfo"
	maxRuntimeGrantsBytes  = 16 << 10
	maxDeploymentFileBytes = 64 << 10
)

// Runner executes one bounded repository command and returns its standard
// output. Implementations must not return unbounded or secret-bearing error
// output to callers.
type Runner func(context.Context, string, []string, string, ...string) ([]byte, error)

// Config identifies the repository state and native platform to package.
type Config struct {
	RepositoryDirectory string
	OutputDirectory     string
	Version             string
	Commit              string
	GOOS                string
	GOARCH              string
}

// Result identifies the immutable archive and its digest.
type Result struct {
	ArchivePath  string
	ChecksumPath string
	SHA256       string
}

type archiveEntry struct {
	name       string
	mode       int64
	sourcePath string
	data       []byte
}

// Build verifies an exact clean native checkout, builds all three release
// executables with one linker identity, executes the database-free identity
// check, and atomically admits a normalized tar.gz plus SHA256SUMS directory.
//
// Complexity: for source/build work b, dependency-manifest bytes d, runtime
// grant bytes g, deployment-file bytes p, executable bytes n, and compressed
// output bytes z, time is O(b+d+g+p+n+z), Omega(n), and auxiliary memory is
// O(d+g+p), Omega(1); executable contents are streamed once rather than
// retained in memory.
func Build(ctx context.Context, configured Config, run Runner) (Result, error) {
	if ctx == nil {
		return Result{}, fmt.Errorf("release context is required")
	}
	if run == nil {
		return Result{}, fmt.Errorf("release command runner is required")
	}
	if err := ctx.Err(); err != nil {
		return Result{}, fmt.Errorf("release build canceled: %w", err)
	}
	if !filepath.IsAbs(configured.RepositoryDirectory) {
		return Result{}, fmt.Errorf("repository directory must be absolute")
	}
	if !filepath.IsAbs(configured.OutputDirectory) {
		return Result{}, fmt.Errorf("output directory must be absolute")
	}
	if configured.RepositoryDirectory == configured.OutputDirectory {
		return Result{}, fmt.Errorf("output directory must differ from repository directory")
	}
	release, err := buildinfo.Validate(configured.Version, configured.Commit)
	if err != nil || release.Version == "development" {
		return Result{}, fmt.Errorf("release identity is invalid")
	}
	if configured.GOOS != runtime.GOOS || configured.GOARCH != runtime.GOARCH {
		return Result{}, fmt.Errorf("release platform must match the native builder")
	}
	if _, err := os.Lstat(configured.OutputDirectory); err == nil {
		return Result{}, fmt.Errorf("output directory already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return Result{}, fmt.Errorf("inspect output directory: %w", err)
	}
	if err := verifyRepository(ctx, run, configured.RepositoryDirectory, configured.Commit); err != nil {
		return Result{}, fmt.Errorf("verify repository before build: %w", err)
	}
	environment := releaseEnvironment(configured.GOOS, configured.GOARCH)
	goVersion, err := commandText(ctx, run, configured.RepositoryDirectory, environment, "go", "env", "GOVERSION")
	if err != nil {
		return Result{}, fmt.Errorf("resolve Go toolchain: %w", err)
	}
	if !strings.HasPrefix(goVersion, "go") || strings.ContainsAny(goVersion, "\x00\r\n") {
		return Result{}, fmt.Errorf("Go toolchain identity is invalid")
	}
	requiredGoVersion, err := commandText(ctx, run, configured.RepositoryDirectory, environment, "go", "list", "-mod=readonly", "-m", "-f", "{{.GoVersion}}")
	if err != nil {
		return Result{}, fmt.Errorf("resolve required Go toolchain: %w", err)
	}
	if goVersion != "go"+requiredGoVersion && !strings.HasPrefix(goVersion, "go"+requiredGoVersion+"-") {
		return Result{}, fmt.Errorf("Go toolchain does not match repository requirement")
	}
	dependencies, err := run(ctx, configured.RepositoryDirectory, environment, "go", "list", "-mod=readonly", "-m", "all")
	if err != nil {
		return Result{}, fmt.Errorf("resolve dependency manifest: %w", err)
	}
	dependencies, err = canonicalManifest(dependencies)
	if err != nil {
		return Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return Result{}, fmt.Errorf("release build canceled: %w", err)
	}
	runtimeGrants, err := run(ctx, configured.RepositoryDirectory, nil, "git", "show", configured.Commit+":deploy/postgresql/runtime-grants.sql")
	if err != nil {
		return Result{}, fmt.Errorf("load runtime grants: %w", err)
	}
	runtimeGrants, err = canonicalRuntimeGrants(runtimeGrants)
	if err != nil {
		return Result{}, err
	}
	deploymentFiles := []struct {
		path string
		mode int64
		data []byte
	}{
		{path: "deploy/container/Containerfile", mode: 0o644},
		{path: "deploy/container/build-image.sh", mode: 0o755},
		{path: "deploy/container/compose.yml", mode: 0o644},
		{path: "deploy/container/entrypoint.sh", mode: 0o755},
		{path: "deploy/postgresql/backup-logical.sh", mode: 0o755},
		{path: "deploy/postgresql/restore-logical.sh", mode: 0o755},
		{path: "deploy/standalone/Caddyfile", mode: 0o644},
		{path: "deploy/standalone/README.md", mode: 0o644},
		{path: "deploy/standalone/app.env.example", mode: 0o644},
		{path: "deploy/standalone/apply-authentik.sh", mode: 0o755},
		{path: "deploy/standalone/authentik/apply.py", mode: 0o644},
		{path: "deploy/standalone/authentik/board-blueprint.yaml", mode: 0o644},
		{path: "deploy/standalone/authentik/permission_matrix.py", mode: 0o644},
		{path: "deploy/standalone/authentik/entrypoint.sh", mode: 0o755},
		{path: "deploy/standalone/compose.yml", mode: 0o644},
		{path: "deploy/standalone/deployment.env.example", mode: 0o644},
		{path: "deploy/standalone/postgresql/init-runtime.sh", mode: 0o755},
		{path: "deploy/standalone/preflight.sh", mode: 0o755},
	}
	for index := range deploymentFiles {
		path := deploymentFiles[index].path
		data, err := run(ctx, configured.RepositoryDirectory, nil, "git", "show", configured.Commit+":"+path)
		if err != nil {
			return Result{}, fmt.Errorf("load release deployment file %s: %w", path, err)
		}
		if len(data) == 0 || len(data) > maxDeploymentFileBytes || data[len(data)-1] != '\n' || bytes.IndexByte(data, 0) >= 0 || bytes.IndexByte(data, '\r') >= 0 {
			return Result{}, fmt.Errorf("release deployment file %s is invalid", path)
		}
		deploymentFiles[index].data = data
	}

	parent := filepath.Dir(configured.OutputDirectory)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return Result{}, fmt.Errorf("create release output parent: %w", err)
	}
	temporary, err := os.MkdirTemp(parent, ".gotth-bb-release-")
	if err != nil {
		return Result{}, fmt.Errorf("create release staging directory: %w", err)
	}
	defer os.RemoveAll(temporary)
	buildDirectory := filepath.Join(temporary, "build")
	stageDirectory := filepath.Join(temporary, "stage")
	if err := os.Mkdir(buildDirectory, 0o755); err != nil {
		return Result{}, fmt.Errorf("create release build directory: %w", err)
	}
	if err := os.Mkdir(stageDirectory, 0o755); err != nil {
		return Result{}, fmt.Errorf("create release artifact directory: %w", err)
	}

	linkerFlags := fmt.Sprintf("-s -w -X=%s.version=%s -X=%s.commit=%s", linkerPackage, release.Version, linkerPackage, release.Commit)
	binaries := []struct{ name, command string }{
		{name: "gotth-bb", command: "./cmd/forum"},
		{name: "gotth-bb-migrate", command: "./cmd/migrate"},
		{name: "gotth-bb-operator", command: "./cmd/operator"},
		{name: "gotth-bb-authentik-gateway", command: "./cmd/authentik-gateway"},
	}
	entries := make([]archiveEntry, 0, len(binaries)+3+len(deploymentFiles))
	root := fmt.Sprintf("gotth-bb-%s-%s-%s", release.Version, configured.GOOS, configured.GOARCH)
	for _, binary := range binaries {
		if err := ctx.Err(); err != nil {
			return Result{}, fmt.Errorf("release build canceled: %w", err)
		}
		output := filepath.Join(buildDirectory, binary.name)
		if _, err := run(ctx, configured.RepositoryDirectory, environment, "go", "build", "-mod=readonly", "-trimpath", "-buildvcs=false", "-ldflags", linkerFlags, "-o", output, binary.command); err != nil {
			return Result{}, fmt.Errorf("build %s: %w", binary.name, err)
		}
		entries = append(entries, archiveEntry{name: root + "/" + binary.name, mode: 0o755, sourcePath: output})
	}
	expectedIdentity := fmt.Sprintf("gotth-bb version=%s commit=%s\n", release.Version, release.Commit)
	for _, binary := range []string{"gotth-bb-migrate", "gotth-bb-operator", "gotth-bb-authentik-gateway"} {
		identity, err := run(ctx, configured.RepositoryDirectory, nil, filepath.Join(buildDirectory, binary), "version")
		if err != nil {
			return Result{}, fmt.Errorf("verify %s release identity: %w", binary, err)
		}
		if string(identity) != expectedIdentity {
			return Result{}, fmt.Errorf("%s release identity does not match artifact", binary)
		}
	}
	if err := verifyRepository(ctx, run, configured.RepositoryDirectory, configured.Commit); err != nil {
		return Result{}, fmt.Errorf("verify repository after build: %w", err)
	}
	metadata := fmt.Sprintf("version=%s\ncommit=%s\ngoos=%s\ngoarch=%s\ngo_version=%s\ngo_required=%s\n", release.Version, release.Commit, configured.GOOS, configured.GOARCH, goVersion, requiredGoVersion)
	entries = append(entries,
		archiveEntry{name: root + "/DEPENDENCIES.txt", mode: 0o644, data: dependencies},
		archiveEntry{name: root + "/RELEASE.txt", mode: 0o644, data: []byte(metadata)},
		archiveEntry{name: root + "/deploy/postgresql/runtime-grants.sql", mode: 0o644, data: runtimeGrants},
	)
	for _, deploymentFile := range deploymentFiles {
		entries = append(entries, archiveEntry{name: root + "/" + deploymentFile.path, mode: deploymentFile.mode, data: deploymentFile.data})
	}
	sort.Slice(entries, func(left, right int) bool { return entries[left].name < entries[right].name })
	archiveName := root + ".tar.gz"
	archivePath := filepath.Join(stageDirectory, archiveName)
	digest, err := writeArchive(archivePath, root, entries)
	if err != nil {
		return Result{}, fmt.Errorf("write release archive: %w", err)
	}
	checksumPath := filepath.Join(stageDirectory, "SHA256SUMS")
	if err := os.WriteFile(checksumPath, []byte(digest+"  "+archiveName+"\n"), 0o644); err != nil {
		return Result{}, fmt.Errorf("write release checksum: %w", err)
	}
	if err := os.Rename(stageDirectory, configured.OutputDirectory); err != nil {
		return Result{}, fmt.Errorf("admit release output: %w", err)
	}
	return Result{
		ArchivePath:  filepath.Join(configured.OutputDirectory, archiveName),
		ChecksumPath: filepath.Join(configured.OutputDirectory, "SHA256SUMS"),
		SHA256:       digest,
	}, nil
}

func verifyRepository(ctx context.Context, run Runner, directory, commit string) error {
	head, err := commandText(ctx, run, directory, nil, "git", "rev-parse", "--verify", "HEAD")
	if err != nil {
		return fmt.Errorf("resolve repository commit: %w", err)
	}
	if head != commit {
		return fmt.Errorf("release commit does not match repository HEAD")
	}
	status, err := run(ctx, directory, nil, "git", "status", "--porcelain=v1", "--untracked-files=normal")
	if err != nil {
		return fmt.Errorf("inspect repository state: %w", err)
	}
	if len(status) != 0 {
		return fmt.Errorf("release repository is dirty")
	}
	return nil
}

func releaseEnvironment(goos, goarch string) []string {
	environment := append(os.Environ(),
		"CGO_ENABLED=0",
		"GOENV=off",
		"GOEXPERIMENT=",
		"GOFIPS140=off",
		"GOFLAGS=",
		"GOTOOLCHAIN=local",
		"GOWORK=off",
		"GOOS="+goos,
		"GOARCH="+goarch,
	)
	variant := ""
	switch goarch {
	case "386":
		variant = "GO386=sse2"
	case "amd64":
		variant = "GOAMD64=v1"
	case "arm":
		variant = "GOARM=7,hardfloat"
	case "arm64":
		variant = "GOARM64=v8.0"
	case "mips", "mipsle":
		variant = "GOMIPS=hardfloat"
	case "mips64", "mips64le":
		variant = "GOMIPS64=hardfloat"
	case "ppc64", "ppc64le":
		variant = "GOPPC64=power8"
	case "riscv64":
		variant = "GORISCV64=rva20u64"
	}
	if variant != "" {
		environment = append(environment, variant)
	}
	return environment
}

func commandText(ctx context.Context, run Runner, directory string, environment []string, name string, args ...string) (string, error) {
	output, err := run(ctx, directory, environment, name, args...)
	if err != nil {
		return "", err
	}
	if len(output) == 0 || output[len(output)-1] != '\n' || strings.Count(string(output), "\n") != 1 {
		return "", fmt.Errorf("command returned a noncanonical single-line result")
	}
	return string(output[:len(output)-1]), nil
}

func canonicalManifest(manifest []byte) ([]byte, error) {
	if len(manifest) == 0 || manifest[len(manifest)-1] != '\n' || strings.ContainsAny(string(manifest), "\x00\r") {
		return nil, fmt.Errorf("dependency manifest is invalid")
	}
	for _, line := range strings.Split(string(manifest[:len(manifest)-1]), "\n") {
		if line == "" {
			return nil, fmt.Errorf("dependency manifest is invalid")
		}
	}
	return manifest, nil
}

func canonicalRuntimeGrants(grants []byte) ([]byte, error) {
	if len(grants) == 0 || len(grants) > maxRuntimeGrantsBytes || grants[len(grants)-1] != '\n' || bytes.IndexByte(grants, 0) >= 0 || bytes.IndexByte(grants, '\r') >= 0 {
		return nil, fmt.Errorf("runtime grants are invalid")
	}
	if bytes.Count(grants, []byte(`:"runtime_role"`)) != 21 {
		return nil, fmt.Errorf("runtime grants are invalid")
	}
	statements := make([]string, 0, 25)
	for _, line := range strings.Split(string(grants[:len(grants)-1]), "\n") {
		if line == "" || strings.HasPrefix(line, "--") {
			continue
		}
		statements = append(statements, line)
	}
	if strings.Join(statements, "\n") != requiredRuntimeGrantsStatements {
		return nil, fmt.Errorf("runtime grants are invalid")
	}
	return grants, nil
}

const requiredRuntimeGrantsStatements = `REVOKE ALL PRIVILEGES ON ALL TABLES IN SCHEMA public FROM :"runtime_role";
REVOKE ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA public FROM :"runtime_role";
REVOKE ALL PRIVILEGES ON ALL FUNCTIONS IN SCHEMA public FROM :"runtime_role";
GRANT USAGE ON SCHEMA public TO :"runtime_role";
GRANT SELECT ON TABLE
    public.area_groups,
    public.areas,
    public.content_renderer_state,
    public.external_identities,
    public.forum_group_members,
    public.forum_groups,
    public.gotth_schema_migrations,
    public.governance_state,
    public.moderation_actions,
    public.oidc_login_attempts,
    public.pending_registrations,
    public.posts,
    public.report_notes,
    public.reports,
    public.registration_invitations,
    public.search_projection_state,
    public.site_settings,
    public.topic_reads,
    public.topics,
    public.user_warnings,
    public.users,
    public.email_test_state
TO :"runtime_role";
GRANT SELECT (id, user_id, issued_at, last_seen_at, validated_at, expires_at,
              revoked_at, user_agent_hash, ip_prefix)
ON TABLE public.sessions
TO :"runtime_role";
GRANT EXECUTE ON FUNCTION
    public.session_id_for_token(bytea),
    public.revoke_session_by_token(bytea, timestamp with time zone),
    public.revoke_session_for_rotation_by_token(bigint, bytea, timestamp with time zone)
TO :"runtime_role";
GRANT USAGE, SELECT ON SEQUENCE
    public.areas_id_seq,
    public.forum_groups_id_seq,
    public.moderation_actions_id_seq,
    public.posts_id_seq,
    public.report_notes_id_seq,
    public.reports_id_seq,
    public.pending_registrations_id_seq,
    public.sessions_id_seq,
    public.topics_id_seq,
    public.user_warnings_id_seq,
    public.users_id_seq
TO :"runtime_role";
GRANT INSERT, UPDATE ON TABLE
    public.external_identities,
    public.oidc_login_attempts,
    public.topic_reads
TO :"runtime_role";
GRANT INSERT (token_hash, user_id, issued_at, last_seen_at, validated_at,
              expires_at, revoked_at, user_agent_hash, ip_prefix),
      UPDATE (last_seen_at, validated_at, revoked_at)
ON TABLE public.sessions
TO :"runtime_role";
GRANT INSERT, UPDATE ON TABLE
    public.areas,
    public.posts,
    public.reports,
    public.topics
TO :"runtime_role";
GRANT INSERT ON TABLE
    public.moderation_actions,
    public.report_notes,
    public.user_warnings
TO :"runtime_role";
GRANT INSERT (authentik_user_id, authentik_subject, display_name,
              verified_email, status, administration_revision, intake_at,
              decided_at, deciding_administrator_id, transition_request_id,
              reconciliation_class),
      UPDATE (display_name, verified_email, status,
              administration_revision, decided_at,
              deciding_administrator_id, transition_request_id,
              reconciliation_class)
ON TABLE public.pending_registrations
TO :"runtime_role";
GRANT INSERT (idempotency_key, authentik_invitation_name, transition_state,
              delivery_state, flow_identity, expires_at, created_at,
              transitioned_at, created_by, administration_revision,
              request_fingerprint, failure_class),
      UPDATE (transition_state, delivery_state, transitioned_at,
              administration_revision, failure_class)
ON TABLE public.registration_invitations
TO :"runtime_role";
GRANT INSERT (administrator_id, idempotency_key, status, requested_at,
              completed_at, next_allowed_at),
      UPDATE (idempotency_key, status, requested_at, completed_at,
              next_allowed_at)
ON TABLE public.email_test_state
TO :"runtime_role";
GRANT INSERT, DELETE ON TABLE public.area_groups TO :"runtime_role";
GRANT INSERT (display_name, email, avatar_url, created_at, updated_at,
              last_login_at, authentik_sync_state),
      UPDATE (display_name, email, avatar_url, role, suspended_at,
              suspended_until, suspension_reason, muted_until, updated_at,
              last_login_at, administration_revision,
              publication_window_started_at, publication_count,
              authentik_sync_state, authentik_sync_last_attempt_at,
              authentik_sync_next_attempt_at, authentik_sync_failure_class)
ON TABLE public.users
TO :"runtime_role";
GRANT UPDATE (singleton)
ON TABLE public.governance_state
TO :"runtime_role";
GRANT UPDATE (site_name, site_description, brand_theme, rules_markdown,
              rules_html, rules_renderer_version, administration_revision,
              updated_at, registration_mode, maintenance_enabled,
              maintenance_message, publish_rate_limit,
              new_account_publish_rate_limit, publish_window_seconds,
              new_account_period_seconds, session_idle_seconds,
              auth_revalidate_seconds)
ON TABLE public.site_settings
TO :"runtime_role";
GRANT INSERT (name, created_by, created_at, updated_at),
      UPDATE (name, updated_at, administration_revision)
ON TABLE public.forum_groups
TO :"runtime_role";
GRANT INSERT (group_id, user_id, granted_by, created_at),
      DELETE
ON TABLE public.forum_group_members
TO :"runtime_role";`

func writeArchive(path, root string, entries []archiveEntry) (string, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return "", err
	}
	digest := sha256.New()
	gzipWriter, err := gzip.NewWriterLevel(io.MultiWriter(file, digest), gzip.BestCompression)
	if err != nil {
		_ = file.Close()
		return "", err
	}
	gzipWriter.Header.ModTime = time.Time{}
	gzipWriter.Header.OS = 255
	tarWriter := tar.NewWriter(gzipWriter)
	archiveErr := tarWriter.WriteHeader(&tar.Header{
		Name: root + "/", Mode: 0o755, Typeflag: tar.TypeDir, ModTime: time.Unix(0, 0).UTC(), Format: tar.FormatUSTAR,
	})
	for _, entry := range entries {
		if archiveErr != nil {
			break
		}
		archiveErr = writeEntry(tarWriter, entry)
	}
	archiveErr = errors.Join(archiveErr, tarWriter.Close(), gzipWriter.Close(), file.Close())
	if archiveErr != nil {
		return "", archiveErr
	}
	return encodeDigest(digest), nil
}

func writeEntry(writer *tar.Writer, entry archiveEntry) error {
	var (
		reader io.Reader = bytes.NewReader(entry.data)
		size   int64     = int64(len(entry.data))
		file   *os.File
	)
	if entry.sourcePath != "" {
		var err error
		file, err = os.Open(entry.sourcePath)
		if err != nil {
			return err
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("archive source is not a regular file")
		}
		reader, size = file, info.Size()
	}
	header := &tar.Header{
		Name: entry.name, Size: size, Mode: entry.mode, Typeflag: tar.TypeReg,
		ModTime: time.Unix(0, 0).UTC(), Format: tar.FormatUSTAR,
	}
	if err := writer.WriteHeader(header); err != nil {
		return err
	}
	_, err := io.Copy(writer, reader)
	return err
}

func encodeDigest(digest hash.Hash) string {
	return hex.EncodeToString(digest.Sum(nil))
}
