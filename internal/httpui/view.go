package httpui

import (
	"fmt"

	contentrender "github.com/gotthboard/gotth-bb/internal/render"
)

const htmxConfiguration = `{"allowEval":false,"allowScriptTags":false,"historyCacheSize":0,"historyRestoreAsHxRequest":false,"includeIndicatorStyles":false,"reportValidityOfForms":true,"selfRequestsOnly":true,"responseHandling":[{"code":"204","swap":false},{"code":"[23]..","swap":true},{"code":"409","swap":true,"error":true},{"code":"422","swap":true,"error":true},{"code":"[45]..","swap":false,"error":true},{"code":"...","swap":false}]}`

type pageView struct {
	SiteName      string
	Title         string
	CanonicalURL  string
	HomeURL       string
	LoginURL      string
	RegisterURL   string
	LogoutURL     string
	AdminURL      string
	StylesheetURL string
	HTMXURL       string
}

type administratorSetupView struct {
	ActionURL string
	CSRFToken string
}

type areaAdministrationPageView struct {
	ActionURL string
	CSRFToken string
	Areas     []areaAdministrationFormView
	Groups    []areaAdministrationGroupView
	FormError string
}

type areaAdministrationFormView struct {
	ID           int64
	ActionURL    string
	Slug         string
	Name         string
	Description  string
	DisplayOrder string
	Visibility   string
	PostingMode  string
	GroupIDs     []int64
	Reason       string
	Revision     string
	Editing      bool
}

type areaAdministrationGroupView struct {
	ID   int64
	Name string
}

type areaIndexItem struct {
	Name         string
	Description  string
	URL          string
	TopicCount   int64
	PostCount    int64
	LatestTitle  string
	LatestURL    string
	LatestAuthor string
	LatestAt     string
}

type areaTopicListView struct {
	Name        string
	Description string
	Topics      []areaTopicListItem
	Number      int32
	TotalTopics int64
	PreviousURL string
	NextURL     string
	NewTopicURL string
}

type areaTopicListItem struct {
	Title        string
	URL          string
	StateLabel   string
	Pinned       bool
	ReplyLabel   string
	Author       string
	LastActivity string
}

type topicPostPageView struct {
	AreaName    string
	AreaURL     string
	Title       string
	StateLabel  string
	Pinned      bool
	Author      string
	Started     string
	Posts       []topicPostItem
	Number      int32
	TotalPosts  int64
	PreviousURL string
	NextURL     string
	ReplyForm   publishingFormView
	ShowReply   bool
	Moderation  []topicModerationView
}

type topicModerationView struct {
	ActionURL   string
	CSRFToken   string
	SubmitLabel string
}

type topicPostItem struct {
	Anchor          string
	Permalink       string
	Number          int32
	IndentClass     string
	Tombstone       bool
	ParentLabel     string
	ParentURL       string
	Author          string
	AuthorStatusURL string
	Created         string
	Edited          string
	EditURL         string
	DeleteURL       string
	CSRFToken       string
	Revision        string
	Body            contentrender.TrustedHTML
	ReplyForm       publishingFormView
	ShowReply       bool
}

type moderationUserView struct {
	DisplayName      string
	RoleLabel        string
	StatusLabel      string
	SuspensionReason string
	SuspendedAt      string
	SuspendedUntil   string
	MutedUntil       string
	CreatedAt        string
	LastLoginAt      string
	ActionURL        string
	CSRFToken        string
	SubmitLabel      string
}

type publishingFormView struct {
	Heading       string
	ActionURL     string
	PreviewURL    string
	CancelURL     string
	CSRFToken     string
	AreaSlug      string
	ParentPostID  string
	Title         string
	Markdown      string
	TitleError    string
	MarkdownError string
	FormError     string
	Reply         bool
	Edit          bool
	Revision      string
	PreviewBody   contentrender.TrustedHTML
	ShowPreview   bool
}

// newPageView resolves every application-owned shell URL through one validated
// builder and binds the page title to the fixed product identity.
//
// Complexity: for k canonical route segments containing n bytes, time and
// auxiliary space are O(k+n), Omega(1), and tight Theta(k+n) for valid input;
// fixed home and asset route work is constant.
func newPageView(builder URLBuilder, title string, canonicalSegments ...string) (pageView, error) {
	if title == "" {
		return pageView{}, fmt.Errorf("page title is required")
	}
	homeURL, err := builder.Path()
	if err != nil {
		return pageView{}, fmt.Errorf("build home URL: %w", err)
	}
	stylesheetURL, err := builder.Path("static", appStylesheetFilename)
	if err != nil {
		return pageView{}, fmt.Errorf("build stylesheet URL: %w", err)
	}
	htmxURL, err := builder.Path("static", "htmx-2.0.10.min.js")
	if err != nil {
		return pageView{}, fmt.Errorf("build HTMX URL: %w", err)
	}
	loginURL, err := builder.Path("login")
	if err != nil {
		return pageView{}, fmt.Errorf("build login URL: %w", err)
	}
	registerURL, err := builder.Path("register")
	if err != nil {
		return pageView{}, fmt.Errorf("build registration URL: %w", err)
	}
	logoutURL, err := builder.Path("logout")
	if err != nil {
		return pageView{}, fmt.Errorf("build logout URL: %w", err)
	}
	adminURL, err := builder.Path("admin", "areas")
	if err != nil {
		return pageView{}, fmt.Errorf("build area administration URL: %w", err)
	}
	canonicalURL, err := builder.Absolute(canonicalSegments...)
	if err != nil {
		return pageView{}, fmt.Errorf("build canonical URL: %w", err)
	}
	return pageView{
		SiteName:      "GOTTH Board",
		Title:         title,
		CanonicalURL:  canonicalURL,
		HomeURL:       homeURL,
		LoginURL:      loginURL,
		RegisterURL:   registerURL,
		LogoutURL:     logoutURL,
		AdminURL:      adminURL,
		StylesheetURL: stylesheetURL,
		HTMXURL:       htmxURL,
	}, nil
}
