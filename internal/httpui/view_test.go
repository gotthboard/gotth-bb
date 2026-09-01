package httpui

import (
	"net/url"
	"testing"
)

func TestNewPageViewBuildsEveryApplicationURL(t *testing.T) {
	t.Parallel()

	builder, err := NewURLBuilder(url.URL{Scheme: "https", Host: "forum.example.test", Path: "/community/board"}, "/community/board")
	if err != nil {
		t.Fatalf("NewURLBuilder() returned error: %v", err)
	}
	view, err := newPageView(builder, "Topic", "topics", "01JTEST")
	if err != nil {
		t.Fatalf("newPageView() returned error: %v", err)
	}
	if view.SiteName != "GOTTH Board" || view.Title != "Topic" {
		t.Fatalf("page identity = (%q, %q)", view.SiteName, view.Title)
	}
	if view.HomeURL != "/community/board/" || view.StylesheetURL != "/community/board/static/app-1.0.0-alpha.1.css" || view.HTMXURL != "/community/board/static/htmx-2.0.10.min.js" {
		t.Fatalf("application URLs = (%q, %q, %q)", view.HomeURL, view.StylesheetURL, view.HTMXURL)
	}
	if view.CanonicalURL != "https://forum.example.test/community/board/topics/01JTEST" {
		t.Fatalf("canonical URL = %q", view.CanonicalURL)
	}
}

func TestNewPageViewRejectsUninitializedBuilder(t *testing.T) {
	t.Parallel()

	if got, err := newPageView(URLBuilder{}, "Invalid"); err == nil {
		t.Fatalf("newPageView() = (%+v, nil), want error", got)
	}
}

func TestNewPageViewRejectsEmptyTitle(t *testing.T) {
	t.Parallel()

	builder, err := NewURLBuilder(url.URL{Scheme: "https", Host: "forum.example.test", Path: "/bb"}, "/bb")
	if err != nil {
		t.Fatalf("NewURLBuilder() returned error: %v", err)
	}
	if got, err := newPageView(builder, ""); err == nil {
		t.Fatalf("newPageView() = (%+v, nil), want error", got)
	}
}
