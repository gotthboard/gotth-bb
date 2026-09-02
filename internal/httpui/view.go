package httpui

import "fmt"

const htmxConfiguration = `{"allowEval":false,"allowScriptTags":false,"historyCacheSize":0,"historyRestoreAsHxRequest":false,"includeIndicatorStyles":false,"reportValidityOfForms":true,"selfRequestsOnly":true,"responseHandling":[{"code":"204","swap":false},{"code":"[23]..","swap":true},{"code":"422","swap":true,"error":true},{"code":"[45]..","swap":false,"error":true},{"code":"...","swap":false}]}`

type pageView struct {
	SiteName      string
	Title         string
	CanonicalURL  string
	HomeURL       string
	StylesheetURL string
	HTMXURL       string
}

type areaIndexItem struct {
	Name        string
	Description string
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
	stylesheetURL, err := builder.Path("static", "app-1.0.0-alpha.1.css")
	if err != nil {
		return pageView{}, fmt.Errorf("build stylesheet URL: %w", err)
	}
	htmxURL, err := builder.Path("static", "htmx-2.0.10.min.js")
	if err != nil {
		return pageView{}, fmt.Errorf("build HTMX URL: %w", err)
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
		StylesheetURL: stylesheetURL,
		HTMXURL:       htmxURL,
	}, nil
}
