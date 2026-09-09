package authentikcontrol

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

const (
	testOpenFlow       = "11111111-1111-4111-8111-111111111111"
	testApprovalFlow   = "22222222-2222-4222-8222-222222222222"
	testInvitationFlow = "33333333-3333-4333-8333-333333333333"
	testAcceptedGroup  = "44444444-4444-4444-8444-444444444444"
	testPendingGroup   = "55555555-5555-4555-8555-555555555555"
	testSuspendedGroup = "66666666-6666-4666-8666-666666666666"
	testUserUUID       = "77777777-7777-4777-8777-777777777777"
	testInvitationUUID = "88888888-8888-4888-8888-888888888888"
)

func testObjects(origin string) Objects {
	return Objects{Version: 1, IssuerOrigin: origin,
		Flows: FlowObjects{
			Open:       Object{Slug: "gotth-bb-open", UUID: testOpenFlow},
			Approval:   Object{Slug: "gotth-bb-approval", UUID: testApprovalFlow},
			Invitation: Object{Slug: "gotth-bb-invitation", UUID: testInvitationFlow},
		},
		Groups: GroupObjects{Accepted: testAcceptedGroup, Pending: testPendingGroup, Suspended: testSuspendedGroup},
	}
}

func testClient(t *testing.T, handler http.Handler) (*Client, *httptest.Server) {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	origin, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	client := &Client{origin: origin, token: Secret{bytes: []byte("control-token")}, objects: testObjects(server.URL), http: server.Client()}
	client.http.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return client, server
}

func TestUserByUUIDUsesOnlyPinnedQuery(t *testing.T) {
	client, server := testClient(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/api/v3/core/users/" || request.URL.Query().Get("uuid") != testUserUUID || request.URL.Query().Get("page_size") != "2" || request.Header.Get("Authorization") != "Bearer control-token" {
			t.Errorf("unexpected request: %s %s", request.Method, request.URL.RequestURI())
		}
		_, _ = io.WriteString(response, `{"pagination":{"next":0},"results":[{"pk":17,"uuid":"`+testUserUUID+`","username":"member","name":"Member","email":"member@example.test","is_active":true}]}`)
	}))
	defer server.Close()
	defer client.Close()
	user, found, err := client.UserByUUID(context.Background(), testUserUUID)
	if err != nil || !found || user.PK != 17 || user.UUID != testUserUUID {
		t.Fatalf("unexpected result: %#v %v %v", user, found, err)
	}
	if _, _, err := client.UserByUUID(context.Background(), "not-a-uuid"); err == nil {
		t.Fatal("invalid UUID accepted")
	}
}

func TestPendingUsersAndGroupsArePinnedAndBounded(t *testing.T) {
	requests := 0
	client, server := testClient(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requests++
		switch requests {
		case 1:
			if request.URL.Path != "/api/v3/core/users/" || request.URL.Query().Get("groups_by_pk") != testPendingGroup || request.URL.Query().Get("page_size") != "51" {
				t.Errorf("unbounded pending query: %s", request.URL.RequestURI())
			}
			_, _ = io.WriteString(response, `{"pagination":{"next":2},"results":[]}`)
		case 2, 3, 4:
			expected := []string{testAcceptedGroup, testPendingGroup, testSuspendedGroup}[requests-2]
			if request.URL.Path != "/api/v3/core/groups/"+expected+"/" || request.URL.Query().Get("include_users") != "false" {
				t.Errorf("unexpected group request: %s", request.URL.RequestURI())
			}
			_, _ = fmt.Fprintf(response, `{"pk":%q,"name":"group"}`, expected)
		default:
			t.Errorf("unexpected request count %d", requests)
		}
	}))
	defer server.Close()
	defer client.Close()
	users, more, err := client.PendingUsers(context.Background())
	if err != nil || len(users) != 0 || !more {
		t.Fatalf("unexpected pending result: %v %v %v", users, more, err)
	}
	if _, _, _, err := client.Groups(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestMembershipRejectsUnpinnedGroup(t *testing.T) {
	requests := 0
	client, server := testClient(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requests++
		if request.Method != http.MethodPost || request.URL.Path != "/api/v3/core/groups/"+testAcceptedGroup+"/add_user/" {
			t.Errorf("unexpected membership request: %s %s", request.Method, request.URL.Path)
		}
		var body struct {
			PK int64 `json:"pk"`
		}
		if json.NewDecoder(request.Body).Decode(&body) != nil || body.PK != 17 {
			t.Errorf("unexpected membership body")
		}
		response.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	defer client.Close()
	if err := client.AddUser(context.Background(), testAcceptedGroup, 17); err != nil {
		t.Fatal(err)
	}
	if err := client.AddUser(context.Background(), testUserUUID, 17); err == nil {
		t.Fatal("unpinned group accepted")
	}
	if requests != 1 {
		t.Fatalf("got %d network requests", requests)
	}
}

func TestUserInGroupUsesExactPinnedFilter(t *testing.T) {
	client, server := testClient(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		query := request.URL.Query()
		if request.Method != http.MethodGet || request.URL.Path != "/api/v3/core/users/" ||
			query.Get("uuid") != testUserUUID || query.Get("groups_by_pk") != testAcceptedGroup ||
			query.Get("include_groups") != "false" || query.Get("include_roles") != "false" || query.Get("page_size") != "2" {
			t.Errorf("unexpected membership verification: %s %s", request.Method, request.URL.RequestURI())
		}
		_, _ = io.WriteString(response, `{"pagination":{"next":0},"results":[{"pk":17,"uuid":"`+testUserUUID+`","username":"member","name":"Member","email":"member@example.test","is_active":true}]}`)
	}))
	defer server.Close()
	defer client.Close()
	member, err := client.UserInGroup(context.Background(), testUserUUID, testAcceptedGroup)
	if err != nil || !member {
		t.Fatalf("UserInGroup() = (%t, %v)", member, err)
	}
	if _, err := client.UserInGroup(context.Background(), testUserUUID, testUserUUID); err == nil {
		t.Fatal("unpinned group accepted")
	}
}

func TestInvitationOperationsForceFlowAndSingleUse(t *testing.T) {
	requests := 0
	client, server := testClient(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requests++
		switch requests {
		case 1:
			if request.Method != http.MethodPost || request.URL.Path != "/api/v3/stages/invitation/invitations/" {
				t.Errorf("unexpected create request")
			}
			var body struct {
				Flow      string            `json:"flow"`
				SingleUse bool              `json:"single_use"`
				FixedData map[string]string `json:"fixed_data"`
			}
			if json.NewDecoder(request.Body).Decode(&body) != nil || body.Flow != testInvitationFlow || !body.SingleUse || body.FixedData["email"] != "invitee@example.test" {
				t.Errorf("create was not pinned: %#v", body)
			}
			response.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(response, `{"pk":"`+testInvitationUUID+`","name":"invite-1","expires":"2026-09-10T00:00:00Z","fixed_data":{"email":"invitee@example.test","name":"Invitee"},"single_use":true,"flow":"`+testInvitationFlow+`"}`)
		case 2:
			if request.Method != http.MethodGet || request.URL.Query().Get("flow__slug") != "gotth-bb-invitation" || request.URL.Query().Get("page_size") != "51" {
				t.Errorf("list was not pinned: %s", request.URL.RequestURI())
			}
			_, _ = io.WriteString(response, `{"pagination":{"next":0},"results":[]}`)
		case 3:
			if request.Method != http.MethodGet || request.URL.Path != "/api/v3/stages/invitation/invitations/"+testInvitationUUID+"/" {
				t.Errorf("retrieve was not exact")
			}
			_, _ = io.WriteString(response, `{"pk":"`+testInvitationUUID+`","name":"invite-1","expires":"2026-09-10T00:00:00Z","fixed_data":{"email":"invitee@example.test"},"single_use":true,"flow":"`+testInvitationFlow+`"}`)
		case 4:
			if request.Method != http.MethodDelete || request.URL.Path != "/api/v3/stages/invitation/invitations/"+testInvitationUUID+"/" {
				t.Errorf("delete was not exact")
			}
			response.WriteHeader(http.StatusNoContent)
		}
	}))
	defer server.Close()
	defer client.Close()
	if _, err := client.CreateInvitation(context.Background(), "invite-1", "2026-09-10T00:00:00Z", "invitee@example.test", "Invitee"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := client.Invitations(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := client.DeleteInvitation(context.Background(), testInvitationUUID); err != nil {
		t.Fatal(err)
	}
}

func TestClientRejectsRedirectBodiesAndOversize(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
	}{
		{"redirect", func(response http.ResponseWriter, _ *http.Request) {
			response.Header().Set("Location", "https://example.test/")
			response.WriteHeader(http.StatusFound)
		}},
		{"status body", func(response http.ResponseWriter, _ *http.Request) {
			response.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(response, "secret remote body")
		}},
		{"oversize", func(response http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(response, `{"padding":"`+strings.Repeat("x", maximumResponseBytes)+`"}`)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client, server := testClient(t, test.handler)
			defer server.Close()
			defer client.Close()
			_, _, err := client.UserByUUID(context.Background(), testUserUUID)
			if err == nil || strings.Contains(err.Error(), "secret remote body") {
				t.Fatalf("unsafe result: %v", err)
			}
		})
	}
}
