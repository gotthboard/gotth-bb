package render

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gotthboard/gotth-bb/internal/abuse"
)

func TestRenderMarkdownForPublicationChecksResolvedDestinationsOnRenderedAST(t *testing.T) {
	t.Parallel()
	policy := destinationPolicyForRenderTest(t, "domain=blocked.example\n")
	blocked := []string{
		"[inline](https://blocked.example/path)",
		"[reference][target]\n\n[target]: https://blocked.example/path",
		"[collapsed][]\n\n[collapsed]: https://blocked.example/path",
		"![image](https://blocked.example/image.png)",
		"https://blocked.example/autolink",
	}
	for _, source := range blocked {
		if rendered, err := RenderMarkdownForPublication(source, policy); !errors.Is(err, abuse.ErrBlockedDestination) || rendered.valid() {
			t.Fatalf("RenderMarkdownForPublication(%q) = (%+v, %v)", source, rendered, err)
		}
	}
	allowed := []string{
		"`https://blocked.example/code`",
		"```\nhttps://blocked.example/fence\n```",
		"https://allowed.example/path",
		"[local](/path) [anchor](#part) [mail](mailto:user@blocked.example)",
		`\[escaped text]`,
		`<a href="https://blocked.example/raw">raw HTML is inert</a>`,
	}
	for _, source := range allowed {
		if rendered, err := RenderMarkdownForPublication(source, policy); err != nil || !rendered.valid() {
			t.Fatalf("RenderMarkdownForPublication(%q) = (%+v, %v)", source, rendered, err)
		}
	}
}

func TestRenderMarkdownForPublicationAcceptsMaximumSourceAndRepeatedAllowedDestinations(t *testing.T) {
	t.Parallel()
	policy := destinationPolicyForRenderTest(t, "domain=blocked.example\n")
	prefix := strings.Repeat("[allowed](https://allowed.example/path) ", 32) + "\n\n"
	source := prefix + strings.Repeat("a", MaximumMarkdownBytes-len(prefix))
	if len(source) != MaximumMarkdownBytes {
		t.Fatalf("maximum source length = %d", len(source))
	}
	if rendered, err := RenderMarkdownForPublication(source, policy); err != nil || !rendered.valid() {
		t.Fatalf("maximum RenderMarkdownForPublication() = (%+v, %v)", rendered, err)
	}
}

func TestRenderMarkdownForPublicationRequiresExplicitPolicy(t *testing.T) {
	t.Parallel()
	if rendered, err := RenderMarkdownForPublication("body", abuse.DestinationPolicy{}); err == nil || rendered.valid() {
		t.Fatalf("RenderMarkdownForPublication(zero policy) = (%+v, %v)", rendered, err)
	}
	if rendered, err := RenderMarkdownForPublication("body", abuse.NewEmptyDestinationPolicy()); err != nil || !rendered.valid() {
		t.Fatalf("RenderMarkdownForPublication(empty policy) = (%+v, %v)", rendered, err)
	}
}

func TestRenderMarkdownForPublicationRejectsAmbiguousResolvedLinks(t *testing.T) {
	t.Parallel()
	policy := abuse.NewEmptyDestinationPolicy()
	for _, source := range []string{
		"[network](//attacker.example/path)",
		`[backslash](https:\\attacker.example\path)`,
		`[mixed](/\attacker.example/path)`,
		"[malformed](http:attacker.example/path)",
		"![network image](//attacker.example/image.png)",
	} {
		if rendered, err := RenderMarkdownForPublication(source, policy); !errors.Is(err, abuse.ErrBlockedDestination) || rendered.valid() {
			t.Fatalf("RenderMarkdownForPublication(%q) = (%+v, %v), want blocked", source, rendered, err)
		}
	}
}

func destinationPolicyForRenderTest(t *testing.T, rules string) abuse.DestinationPolicy {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rules")
	if err := os.WriteFile(path, []byte(rules), 0o400); err != nil {
		t.Fatalf("write rules: %v", err)
	}
	policy, err := abuse.LoadPolicy(path, abuse.RateProfile{
		RequestLimit: 1, RequestWindow: time.Minute, RequestClientCapacity: 1,
		PublicationLimit: 1, NewAccountLimit: 1,
		PublicationWindow: time.Minute, NewAccountPeriod: time.Hour,
	})
	if err != nil {
		t.Fatalf("LoadPolicy() returned error: %v", err)
	}
	return policy.DestinationPolicy()
}
