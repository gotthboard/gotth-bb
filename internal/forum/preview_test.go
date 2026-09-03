package forum

import (
	"errors"
	"strings"
	"testing"

	"github.com/gotthboard/gotth-bb/internal/render"
)

func TestRenderTopicDraftUsesPublicationValidationAndRenderer(t *testing.T) {
	t.Parallel()

	rendered, err := RenderTopicDraft("member-news", "Careful title", "Hello **world**")
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
		{name: "area", run: func() error { _, err := RenderTopicDraft("bad area", "Title", "body"); return err }, wantField: "area"},
		{name: "title", run: func() error { _, err := RenderTopicDraft("news", "", "body"); return err }, wantField: "title"},
		{name: "topic Markdown", run: func() error { _, err := RenderTopicDraft("news", "Title", " "); return err }, wantField: "markdown"},
		{name: "reply Markdown", run: func() error { _, err := RenderReplyDraft(""); return err }, wantField: "markdown"},
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

	rendered, err := RenderReplyDraft("<script>alert(1)</script>\n\nA [safe](https://example.test) link")
	html, _, valuesErr := rendered.PersistenceValues()
	if err != nil || valuesErr != nil || strings.Contains(html, "script") || !strings.Contains(html, `<a href="https://example.test" rel="nofollow noreferrer">safe</a>`) {
		t.Fatalf("RenderReplyDraft() = (%q, %v/%v)", html, err, valuesErr)
	}
}
