package authentikgateway

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gotthboard/gotth-bb/internal/authentikcontrol"
)

const (
	testUser       = "77777777-7777-4777-8777-777777777777"
	testInvitation = "88888888-8888-4888-8888-888888888888"
)

type fakeRemote struct {
	calls       atomic.Int64
	block       <-chan struct{}
	entered     chan<- struct{}
	err         error
	group       string
	userPK      int64
	member      bool
	displayName string
}

func (remote *fakeRemote) wait() {
	remote.calls.Add(1)
	if remote.entered != nil {
		remote.entered <- struct{}{}
	}
	if remote.block != nil {
		<-remote.block
	}
}

func (remote *fakeRemote) UserByUUID(context.Context, string) (authentikcontrol.User, bool, error) {
	remote.wait()
	return authentikcontrol.User{PK: 17, UUID: testUser, Username: "member", Name: "Member", Email: "member@example.test", Active: true}, true, remote.err
}
func (remote *fakeRemote) PendingUsers(context.Context) ([]authentikcontrol.User, bool, error) {
	remote.wait()
	return []authentikcontrol.User{{PK: 17, UUID: testUser}}, false, remote.err
}
func (remote *fakeRemote) UserInGroup(context.Context, string, string) (bool, error) {
	remote.calls.Add(1)
	return remote.member, remote.err
}
func (remote *fakeRemote) Groups(context.Context) (authentikcontrol.Group, authentikcontrol.Group, authentikcontrol.Group, error) {
	remote.wait()
	return authentikcontrol.Group{}, authentikcontrol.Group{}, authentikcontrol.Group{}, remote.err
}
func (remote *fakeRemote) AddUser(_ context.Context, group string, pk int64) error {
	remote.calls.Add(1)
	remote.group, remote.userPK, remote.member = group, pk, true
	return remote.err
}
func (remote *fakeRemote) RemoveUser(_ context.Context, group string, pk int64) error {
	remote.calls.Add(1)
	remote.group, remote.userPK, remote.member = group, pk, false
	return remote.err
}
func (remote *fakeRemote) CreateInvitation(_ context.Context, _, _, _, displayName string) (authentikcontrol.Invitation, error) {
	remote.calls.Add(1)
	remote.displayName = displayName
	return authentikcontrol.Invitation{UUID: testInvitation}, remote.err
}
func (remote *fakeRemote) Invitations(context.Context) ([]authentikcontrol.Invitation, bool, error) {
	remote.wait()
	return nil, false, remote.err
}
func (remote *fakeRemote) Invitation(context.Context, string) (authentikcontrol.Invitation, error) {
	remote.wait()
	return authentikcontrol.Invitation{UUID: testInvitation}, remote.err
}
func (remote *fakeRemote) DeleteInvitation(context.Context, string) error {
	remote.wait()
	return remote.err
}

func testObjects() authentikcontrol.Objects {
	return authentikcontrol.Objects{Groups: authentikcontrol.GroupObjects{
		Accepted:  "11111111-1111-4111-8111-111111111111",
		Pending:   "22222222-2222-4222-8222-222222222222",
		Suspended: "33333333-3333-4333-8333-333333333333",
	}}
}

func request(method, path, body string) *http.Request {
	var source *strings.Reader
	if body == "" {
		source = strings.NewReader("")
	} else {
		source = strings.NewReader(body)
	}
	result := httptest.NewRequest(method, "http://"+fixedHost+path, source)
	result.Host = fixedHost
	result.ProtoMajor, result.ProtoMinor = 1, 1
	if body != "" {
		result.Header.Set("Content-Type", "application/json")
	}
	return result
}

func TestHandlerHealthAndClosedSurface(t *testing.T) {
	remote := &fakeRemote{}
	handler, err := NewHandler(remote, testObjects(), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(*http.Request)
	}{
		{"wrong host", func(request *http.Request) { request.Host = "other" }},
		{"query", func(request *http.Request) { request.URL.RawQuery = "x=1" }},
		{"cookie", func(request *http.Request) { request.Header.Set("Cookie", "x=y") }},
		{"forwarded", func(request *http.Request) { request.Header.Set("X-Forwarded-Client-Cert", "bad") }},
		{"get body", func(request *http.Request) { request.Body = ioNopCloser("x"); request.ContentLength = 1 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := request(http.MethodGet, "/health/live", "")
			test.mutate(req)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, req)
			if response.Code != http.StatusBadRequest || response.Body.String() != "{\"error\":\"invalid_request\"}\n" {
				t.Fatalf("unexpected response: %d %q", response.Code, response.Body.String())
			}
		})
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request(http.MethodGet, "/health/live", ""))
	if response.Code != http.StatusNoContent || response.Body.Len() != 0 || remote.calls.Load() != 0 {
		t.Fatalf("unexpected health response: %d %q calls=%d", response.Code, response.Body.String(), remote.calls.Load())
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request(http.MethodPost, "/v1/invitations/"+testInvitation+"/send_email", `{}`))
	if response.Code != http.StatusBadRequest || remote.calls.Load() != 0 {
		t.Fatalf("generic route reached remote: %d calls=%d", response.Code, remote.calls.Load())
	}
}

func ioNopCloser(value string) *readCloser { return &readCloser{Reader: strings.NewReader(value)} }

type readCloser struct{ *strings.Reader }

func (reader *readCloser) Close() error { return nil }

func TestMembershipPinsGroupAndResolvesUser(t *testing.T) {
	remote := &fakeRemote{}
	handler, _ := NewHandler(remote, testObjects(), time.Now)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request(http.MethodPost, "/v1/groups/accepted/add", `{"user_uuid":"`+testUser+`"}`))
	if response.Code != http.StatusNoContent || remote.group != testObjects().Groups.Accepted || remote.userPK != 17 || remote.calls.Load() != 3 {
		t.Fatalf("unexpected transition: status=%d group=%q pk=%d calls=%d", response.Code, remote.group, remote.userPK, remote.calls.Load())
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request(http.MethodPost, "/v1/groups/accepted/add", `{"user_uuid":"`+testUser+`","user_uuid":"`+testUser+`"}`))
	if response.Code != http.StatusBadRequest || remote.calls.Load() != 3 {
		t.Fatalf("duplicate JSON reached remote: %d calls=%d", response.Code, remote.calls.Load())
	}
}

func TestUserProjectionCarriesImmutableNumericKey(t *testing.T) {
	remote := &fakeRemote{member: true}
	handler, _ := NewHandler(remote, testObjects(), time.Now)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request(http.MethodGet, "/v1/users/"+testUser, ""))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"id":17`) ||
		!strings.Contains(response.Body.String(), `"uuid":"`+testUser+`"`) ||
		!strings.Contains(response.Body.String(), `"accepted":true`) ||
		!strings.Contains(response.Body.String(), `"pending":true`) ||
		!strings.Contains(response.Body.String(), `"suspended":true`) || remote.calls.Load() != 4 {
		t.Fatalf("unexpected user projection: %d %q", response.Code, response.Body.String())
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request(http.MethodGet, "/v1/pending-users", ""))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"id":17`) {
		t.Fatalf("unexpected pending projection: %d %q", response.Code, response.Body.String())
	}
}

func TestInvitationBoundsAndErrorClasses(t *testing.T) {
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	remote := &fakeRemote{}
	handler, _ := NewHandler(remote, testObjects(), func() time.Time { return now })
	body := fmt.Sprintf(`{"name":"invite-1","expires":%q,"email":"invitee@example.test","display_name":"Invitee"}`, now.Add(time.Hour).Format(time.RFC3339))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request(http.MethodPost, "/v1/invitations", body))
	if response.Code != http.StatusCreated || remote.displayName != "Invitee" {
		t.Fatalf("valid invitation rejected: %d %q", response.Code, response.Body.String())
	}
	for _, invalid := range []string{
		strings.Replace(body, "invitee@example.test", "Name <invitee@example.test>", 1),
		strings.Replace(body, now.Add(time.Hour).Format(time.RFC3339), now.Add(time.Minute).Format(time.RFC3339), 1),
		strings.Replace(body, "Invitee", " Invitee", 1),
	} {
		response = httptest.NewRecorder()
		handler.ServeHTTP(response, request(http.MethodPost, "/v1/invitations", invalid))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("invalid invitation accepted: %d", response.Code)
		}
	}
	remote.err = authentikcontrol.ErrRemoteInvalid
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request(http.MethodGet, "/v1/users/"+testUser, ""))
	if response.Code != http.StatusBadGateway || response.Body.String() != "{\"error\":\"remote_invalid\"}\n" {
		t.Fatalf("unexpected remote error: %d %q", response.Code, response.Body.String())
	}
}

func TestHandlerConcurrencyCeiling(t *testing.T) {
	block := make(chan struct{})
	entered := make(chan struct{}, maximumCalls)
	remote := &fakeRemote{block: block, entered: entered}
	handler, _ := NewHandler(remote, testObjects(), time.Now)
	done := make(chan struct{}, maximumCalls)
	for range maximumCalls {
		go func() {
			handler.ServeHTTP(httptest.NewRecorder(), request(http.MethodGet, "/v1/pending-users", ""))
			done <- struct{}{}
		}()
	}
	for range maximumCalls {
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("remote call did not enter")
		}
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request(http.MethodGet, "/v1/pending-users", ""))
	if response.Code != http.StatusServiceUnavailable || remote.calls.Load() != maximumCalls {
		t.Fatalf("ceiling failed: status=%d calls=%d", response.Code, remote.calls.Load())
	}
	close(block)
	for range maximumCalls {
		<-done
	}
}

func TestUnixListenerVerifiesDirectoryPeerAndCleanup(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o750); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "authentik-control.sock")
	uid, gid := uint32(os.Getuid()), uint32(os.Getgid())
	listener, err := listen(path, uid, gid, uid)
	if err != nil {
		t.Fatal(err)
	}
	accepted := make(chan error, 1)
	go func() {
		connection, err := listener.Accept()
		if err == nil {
			_ = connection.Close()
		}
		accepted <- err
	}()
	connection, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	_ = connection.Close()
	if err := <-accepted; err != nil {
		t.Fatal(err)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket not removed: %v", err)
	}
	second, err := listen(path, uid, gid, uid+1)
	if err != nil {
		t.Fatal(err)
	}
	rejected := make(chan error, 1)
	go func() {
		_, err := second.Accept()
		rejected <- err
	}()
	connection, err = net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	_ = connection.SetReadDeadline(time.Now().Add(time.Second))
	var one [1]byte
	if count, err := connection.Read(one[:]); count != 0 || err == nil {
		t.Fatalf("wrong peer UID was not rejected: count=%d err=%v", count, err)
	}
	_ = connection.Close()
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-rejected; err == nil {
		t.Fatal("closed rejected-peer listener returned a connection")
	}
}
