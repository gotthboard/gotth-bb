package forum

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gotthboard/gotth-bb/internal/abuse"
	"github.com/gotthboard/gotth-bb/internal/policy"
	"github.com/gotthboard/gotth-bb/internal/render"
)

func TestRenderTopicDraftUsesPublicationValidationAndRenderer(t *testing.T) {
	t.Parallel()

	rendered, err := RenderTopicDraft(testDestinationPolicy, "member-news", "Careful title", "Hello **world**")
	html, version, valuesErr := rendered.PersistenceValues()
	if err != nil || valuesErr != nil || html != "<p>Hello <strong>world</strong></p>\n" || version != render.RendererVersion {
		t.Fatalf("RenderTopicDraft() = (%q, %q, %v/%v)", html, version, err, valuesErr)
	}
}

func TestRenderDraftsReturnStableFieldErrors(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name      string
		run       func() error
		wantField string
	}{
		{name: "area", run: func() error {
			_, err := RenderTopicDraft(testDestinationPolicy, "bad area", "Title", "body")
			return err
		}, wantField: "area"},
		{name: "title", run: func() error { _, err := RenderTopicDraft(testDestinationPolicy, "news", "", "body"); return err }, wantField: "title"},
		{name: "topic Markdown", run: func() error { _, err := RenderTopicDraft(testDestinationPolicy, "news", "Title", " "); return err }, wantField: "markdown"},
		{name: "reply Markdown", run: func() error { _, err := RenderReplyDraft(testDestinationPolicy, ""); return err }, wantField: "markdown"},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var invalid InvalidPublishingInput
			err := test.run()
			if !errors.As(err, &invalid) || invalid.Field != test.wantField || !errors.Is(err, ErrInvalidPublishingInput) {
				t.Fatalf("draft error = (%v, %+v), want field %q", err, invalid, test.wantField)
			}
		})
	}
}

func TestRenderReplyDraftReturnsOpaqueSanitizedResult(t *testing.T) {
	t.Parallel()

	rendered, err := RenderReplyDraft(testDestinationPolicy, "<script>alert(1)</script>\n\nA [safe](https://example.test) link")
	html, _, valuesErr := rendered.PersistenceValues()
	if err != nil || valuesErr != nil || strings.Contains(html, "script") || !strings.Contains(html, `<a href="https://example.test" rel="nofollow noreferrer">safe</a>`) {
		t.Fatalf("RenderReplyDraft() = (%q, %v/%v)", html, err, valuesErr)
	}
}

func TestBlockedDestinationIsOpaqueMarkdownValidationBeforeTransaction(t *testing.T) {
	t.Parallel()

	destinationPolicy := blockedDestinationPolicy(t)
	actor := policy.AccessContext{Authenticated: true, UserID: 11, Role: policy.RoleMember}
	markdown := "secret [target](https://blocked.example/path)"
	for _, test := range []struct {
		name string
		run  func() error
	}{
		{name: "topic preview", run: func() error { _, err := RenderTopicDraft(destinationPolicy, "news", "Title", markdown); return err }},
		{name: "reply preview", run: func() error { _, err := RenderReplyDraft(destinationPolicy, markdown); return err }},
		{name: "topic publication", run: func() error {
			_, err := CreateTopic(context.Background(), panicPublishBeginner{}, testPublicationPolicy, destinationPolicy, actor, "news", "Title", markdown)
			return err
		}},
		{name: "reply publication", run: func() error {
			_, err := CreateReply(context.Background(), panicPublishBeginner{}, testPublicationPolicy, destinationPolicy, actor, 41, 91, markdown)
			return err
		}},
		{name: "post edit", run: func() error {
			_, err := EditPost(context.Background(), panicPublishBeginner{}, time.Now, destinationPolicy, actor, 91, 3, markdown)
			return err
		}},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := test.run()
			var invalid InvalidPublishingInput
			if !errors.Is(err, abuse.ErrBlockedDestination) || !errors.As(err, &invalid) || invalid.Field != "markdown" ||
				strings.Contains(err.Error(), "blocked.example") || strings.Contains(err.Error(), "secret") {
				t.Fatalf("blocked result = (%v, %+v)", err, invalid)
			}
		})
	}
}

func TestBlockedDestinationHasNoAuthenticatedRoleBypass(t *testing.T) {
	t.Parallel()

	destinationPolicy := blockedDestinationPolicy(t)
	for index, role := range []policy.Role{policy.RoleMember, policy.RoleModerator, policy.RoleAdministrator} {
		actor := policy.AccessContext{Authenticated: true, UserID: int64(index + 1), Role: role}
		if _, err := CreateTopic(
			context.Background(), panicPublishBeginner{}, testPublicationPolicy, destinationPolicy,
			actor, "news", "Title", "[target](https://blocked.example/path)",
		); !errors.Is(err, abuse.ErrBlockedDestination) {
			t.Fatalf("CreateTopic(role %d) error = %v, want blocked destination", role, err)
		}
	}
}

func blockedDestinationPolicy(t *testing.T) abuse.DestinationPolicy {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rules")
	if err := os.WriteFile(path, []byte("domain=blocked.example\n"), 0o400); err != nil {
		t.Fatalf("write destination policy: %v", err)
	}
	loaded, err := abuse.LoadPolicy(path, abuse.RateProfile{
		RequestLimit: 10, RequestWindow: time.Minute, RequestClientCapacity: 10,
		PublicationLimit: 10, NewAccountLimit: 3, PublicationWindow: time.Minute, NewAccountPeriod: time.Hour,
	})
	if err != nil {
		t.Fatalf("LoadPolicy() returned error: %v", err)
	}
	return loaded.DestinationPolicy()
}
