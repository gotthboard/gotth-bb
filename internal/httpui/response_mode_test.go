package httpui

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestSelectResponseModeUsesExactHTMXRequestContract(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		hxRequest      string
		historyRestore string
		want           responseMode
	}{
		{name: "ordinary page", want: responseModePage},
		{name: "htmx fragment", hxRequest: "true", want: responseModeFragment},
		{name: "noncanonical request header", hxRequest: "TRUE", want: responseModePage},
		{name: "history restore", hxRequest: "true", historyRestore: "true", want: responseModePage},
		{name: "explicit non-history htmx", hxRequest: "true", historyRestore: "false", want: responseModeFragment},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			request := httptest.NewRequest(http.MethodGet, "/", nil)
			if test.hxRequest != "" {
				request.Header.Set("HX-Request", test.hxRequest)
			}
			if test.historyRestore != "" {
				request.Header.Set("HX-History-Restore-Request", test.historyRestore)
			}
			response := httptest.NewRecorder()
			response.Header().Add("Vary", "Accept-Encoding")
			if got := selectResponseMode(response, request); got != test.want {
				t.Fatalf("selectResponseMode() = %d, want %d", got, test.want)
			}
			wantVary := []string{"Accept-Encoding", "HX-Request", "HX-History-Restore-Request"}
			if got := response.Header().Values("Vary"); !reflect.DeepEqual(got, wantVary) {
				t.Fatalf("Vary = %q, want %q", got, wantVary)
			}
		})
	}
}
