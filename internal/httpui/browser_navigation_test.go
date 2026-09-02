package httpui

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestBrowserNavigationSeparatesMutationAndSessionBoundaries(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name, hxRequest, historyRestore string
		navigate                        func(http.ResponseWriter, *http.Request, string)
		wantStatus                      int
		wantHeaders                     http.Header
	}{
		{
			name: "ordinary mutation", navigate: serveMutationNavigation, wantStatus: http.StatusSeeOther,
			wantHeaders: http.Header{"Location": {"/bb/topics/41"}},
		},
		{
			name: "HTMX mutation", hxRequest: "true", navigate: serveMutationNavigation, wantStatus: http.StatusNoContent,
			wantHeaders: http.Header{"Hx-Location": {`{"path":"/bb/topics/41","target":"#main-content","swap":"outerHTML"}`}},
		},
		{
			name: "HTMX history restoration", hxRequest: "true", historyRestore: "true", navigate: serveMutationNavigation, wantStatus: http.StatusSeeOther,
			wantHeaders: http.Header{"Location": {"/bb/topics/41"}},
		},
		{
			name: "ordinary session", navigate: serveSessionRedirect, wantStatus: http.StatusSeeOther,
			wantHeaders: http.Header{"Location": {"/bb/topics/41"}},
		},
		{
			name: "HTMX session", hxRequest: "true", navigate: serveSessionRedirect, wantStatus: http.StatusNoContent,
			wantHeaders: http.Header{"Hx-Redirect": {"/bb/topics/41"}},
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			request := httptest.NewRequest(http.MethodPost, "/mutation", nil)
			request.Header.Set("HX-Request", test.hxRequest)
			request.Header.Set("HX-History-Restore-Request", test.historyRestore)
			response := httptest.NewRecorder()
			test.navigate(response, request, "/bb/topics/41")

			for name, values := range test.wantHeaders {
				if got := response.Header().Values(name); !reflect.DeepEqual(got, values) {
					t.Fatalf("header %s = %q, want %q", name, got, values)
				}
			}
			if response.Code != test.wantStatus || response.Body.Len() != 0 {
				t.Fatalf("response = (status %d, headers %v, body %q)", response.Code, response.Header(), response.Body.String())
			}
			for _, absent := range []string{"HX-Location", "HX-Redirect", "Location"} {
				if _, expected := test.wantHeaders[http.CanonicalHeaderKey(absent)]; !expected && response.Header().Get(absent) != "" {
					t.Fatalf("unexpected %s header %q", absent, response.Header().Get(absent))
				}
			}
		})
	}
}
