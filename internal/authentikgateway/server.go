package authentikgateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/mail"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/gotthboard/gotth-bb/internal/authentikcontrol"
	"golang.org/x/sys/unix"
)

const (
	boardUID     = 65532
	controlGID   = 65531
	gatewayUID   = 65533
	maximumBody  = 8 << 10
	maximumCalls = 8
	fixedHost    = "gotth-bb-authentik-control"
)

var canonicalUUID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
var canonicalSlug = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

// Remote is the complete outbound authority available to the gateway.
type Remote interface {
	UserByUUID(context.Context, string) (authentikcontrol.User, bool, error)
	PendingUsers(context.Context) ([]authentikcontrol.User, bool, error)
	Groups(context.Context) (authentikcontrol.Group, authentikcontrol.Group, authentikcontrol.Group, error)
	AddUser(context.Context, string, int64) error
	RemoveUser(context.Context, string, int64) error
	CreateInvitation(context.Context, string, string, string, string) (authentikcontrol.Invitation, error)
	Invitations(context.Context) ([]authentikcontrol.Invitation, bool, error)
	Invitation(context.Context, string) (authentikcontrol.Invitation, error)
	DeleteInvitation(context.Context, string) error
}

type Handler struct {
	remote  Remote
	objects authentikcontrol.Objects
	clock   func() time.Time
	calls   chan struct{}
}

type User struct {
	UUID     string `json:"uuid"`
	Username string `json:"username"`
	Name     string `json:"name"`
	Email    string `json:"email"`
	Active   bool   `json:"active"`
}

type Invitation struct {
	UUID        string `json:"uuid"`
	Name        string `json:"name"`
	Expires     string `json:"expires"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
	SingleUse   bool   `json:"single_use"`
}

func NewHandler(remote Remote, objects authentikcontrol.Objects, clock func() time.Time) (*Handler, error) {
	if remote == nil || clock == nil {
		return nil, fmt.Errorf("gateway dependencies are required")
	}
	return &Handler{remote: remote, objects: objects, clock: clock, calls: make(chan struct{}, maximumCalls)}, nil
}

func (handler *Handler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("Content-Type", "application/json")
	invalidHeaders := request.Header.Get("Cookie") != ""
	for name := range request.Header {
		lower := strings.ToLower(name)
		if lower == "forwarded" || strings.HasPrefix(lower, "x-forwarded-") {
			invalidHeaders = true
		}
	}
	noBodyMethod := request.Method == http.MethodGet || request.Method == http.MethodDelete
	if request.ProtoMajor != 1 || request.ProtoMinor != 1 || request.Host != fixedHost || request.URL.RawQuery != "" || invalidHeaders || (noBodyMethod && (request.ContentLength > 0 || len(request.TransferEncoding) != 0 || request.Header.Get("Content-Type") != "")) {
		writeError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	if request.Method == http.MethodGet && request.URL.Path == "/health/live" {
		response.Header().Del("Content-Type")
		response.WriteHeader(http.StatusNoContent)
		return
	}
	select {
	case handler.calls <- struct{}{}:
		defer func() { <-handler.calls }()
	default:
		writeError(response, http.StatusServiceUnavailable, "remote_unavailable")
		return
	}
	handler.route(response, request)
}

func (handler *Handler) route(response http.ResponseWriter, request *http.Request) {
	switch {
	case request.Method == http.MethodGet && request.URL.Path == "/v1/pending-users":
		users, more, err := handler.remote.PendingUsers(request.Context())
		projected := make([]User, len(users))
		for index := range users {
			projected[index] = projectUser(users[index])
		}
		writeRemote(response, http.StatusOK, struct {
			Users []User `json:"users"`
			More  bool   `json:"more"`
		}{projected, more}, err)
	case request.Method == http.MethodGet && request.URL.Path == "/v1/groups":
		accepted, pending, suspended, err := handler.remote.Groups(request.Context())
		writeRemote(response, http.StatusOK, struct {
			Accepted  authentikcontrol.Group `json:"accepted"`
			Pending   authentikcontrol.Group `json:"pending"`
			Suspended authentikcontrol.Group `json:"suspended"`
		}{accepted, pending, suspended}, err)
	case request.Method == http.MethodGet && strings.HasPrefix(request.URL.Path, "/v1/users/"):
		uuid := strings.TrimPrefix(request.URL.Path, "/v1/users/")
		if !canonicalUUID.MatchString(uuid) {
			writeError(response, http.StatusBadRequest, "invalid_request")
			return
		}
		user, found, err := handler.remote.UserByUUID(request.Context(), uuid)
		if err == nil && !found {
			err = authentikcontrol.ErrAbsent
		}
		writeRemote(response, http.StatusOK, projectUser(user), err)
	case strings.HasPrefix(request.URL.Path, "/v1/groups/"):
		handler.membership(response, request)
	case request.URL.Path == "/v1/invitations" && request.Method == http.MethodGet:
		invitations, more, err := handler.remote.Invitations(request.Context())
		projected := make([]Invitation, len(invitations))
		for index := range invitations {
			projected[index] = projectInvitation(invitations[index])
		}
		writeRemote(response, http.StatusOK, struct {
			Invitations []Invitation `json:"invitations"`
			More        bool         `json:"more"`
		}{projected, more}, err)
	case request.URL.Path == "/v1/invitations" && request.Method == http.MethodPost:
		handler.createInvitation(response, request)
	case strings.HasPrefix(request.URL.Path, "/v1/invitations/") && (request.Method == http.MethodGet || request.Method == http.MethodDelete):
		handler.invitation(response, request)
	default:
		writeError(response, http.StatusBadRequest, "invalid_request")
	}
}

func (handler *Handler) membership(response http.ResponseWriter, request *http.Request) {
	parts := strings.Split(strings.TrimPrefix(request.URL.Path, "/v1/groups/"), "/")
	if request.Method != http.MethodPost || len(parts) != 2 {
		writeError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	groups := map[string]string{"accepted": handler.objects.Groups.Accepted, "pending": handler.objects.Groups.Pending, "suspended": handler.objects.Groups.Suspended}
	group, ok := groups[parts[0]]
	if !ok || (parts[1] != "add" && parts[1] != "remove") {
		writeError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	var body struct {
		UserUUID string `json:"user_uuid"`
	}
	if err := decodeBody(response, request, &body); err != nil || !canonicalUUID.MatchString(body.UserUUID) {
		writeError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	user, found, err := handler.remote.UserByUUID(request.Context(), body.UserUUID)
	if err == nil && !found {
		err = authentikcontrol.ErrAbsent
	}
	if err == nil {
		if parts[1] == "add" {
			err = handler.remote.AddUser(request.Context(), group, user.PK)
		} else {
			err = handler.remote.RemoveUser(request.Context(), group, user.PK)
		}
	}
	if err != nil {
		writeRemote(response, 0, nil, err)
		return
	}
	response.Header().Del("Content-Type")
	response.WriteHeader(http.StatusNoContent)
}

func (handler *Handler) createInvitation(response http.ResponseWriter, request *http.Request) {
	var body struct {
		Name        string `json:"name"`
		Expires     string `json:"expires"`
		Email       string `json:"email"`
		DisplayName string `json:"display_name"`
	}
	if err := decodeBody(response, request, &body); err != nil || !validInvitation(body.Name, body.Expires, body.Email, body.DisplayName, handler.clock()) {
		writeError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	invitation, err := handler.remote.CreateInvitation(request.Context(), body.Name, body.Expires, body.Email, body.DisplayName)
	writeRemote(response, http.StatusCreated, projectInvitation(invitation), err)
}

func (handler *Handler) invitation(response http.ResponseWriter, request *http.Request) {
	uuid := strings.TrimPrefix(request.URL.Path, "/v1/invitations/")
	if !canonicalUUID.MatchString(uuid) {
		writeError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	if request.Method == http.MethodGet {
		invitation, err := handler.remote.Invitation(request.Context(), uuid)
		writeRemote(response, http.StatusOK, projectInvitation(invitation), err)
		return
	}
	err := handler.remote.DeleteInvitation(request.Context(), uuid)
	if err != nil {
		writeRemote(response, 0, nil, err)
		return
	}
	response.Header().Del("Content-Type")
	response.WriteHeader(http.StatusNoContent)
}

func projectUser(user authentikcontrol.User) User {
	return User{UUID: user.UUID, Username: user.Username, Name: user.Name, Email: user.Email, Active: user.Active}
}

func projectInvitation(invitation authentikcontrol.Invitation) Invitation {
	email, _ := invitation.FixedData["email"].(string)
	displayName, _ := invitation.FixedData["name"].(string)
	return Invitation{UUID: invitation.UUID, Name: invitation.Name, Expires: invitation.Expires, Email: email, DisplayName: displayName, SingleUse: invitation.SingleUse}
}

func decodeBody(response http.ResponseWriter, request *http.Request, destination any) error {
	if request.Header.Get("Content-Type") != "application/json" || request.Body == nil || request.ContentLength > maximumBody {
		return errors.New("invalid body")
	}
	raw, err := io.ReadAll(http.MaxBytesReader(response, request.Body, maximumBody+1))
	if err != nil || len(raw) == 0 || len(raw) > maximumBody || duplicateObjectKey(raw) {
		return errors.New("invalid body")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return errors.New("invalid body")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("invalid body")
	}
	return nil
}

func duplicateObjectKey(raw []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var walk func() bool
	walk = func() bool {
		token, err := decoder.Token()
		if err != nil {
			return true
		}
		delimiter, compound := token.(json.Delim)
		if !compound {
			return false
		}
		switch delimiter {
		case '{':
			seen := make(map[string]struct{})
			for decoder.More() {
				keyToken, err := decoder.Token()
				key, ok := keyToken.(string)
				if err != nil || !ok {
					return true
				}
				if _, exists := seen[key]; exists {
					return true
				}
				seen[key] = struct{}{}
				if walk() {
					return true
				}
			}
			end, err := decoder.Token()
			return err != nil || end != json.Delim('}')
		case '[':
			for decoder.More() {
				if walk() {
					return true
				}
			}
			end, err := decoder.Token()
			return err != nil || end != json.Delim(']')
		default:
			return true
		}
	}
	if walk() {
		return true
	}
	var trailing any
	return decoder.Decode(&trailing) != io.EOF
}

func validInvitation(name, expires, email, displayName string, now time.Time) bool {
	if name == "" || len(name) > 150 || !canonicalSlug.MatchString(name) || len(email) == 0 || len(email) > 320 || !utf8.ValidString(email) || strings.ContainsAny(email, " \t\r\n") {
		return false
	}
	address, err := mail.ParseAddress(email)
	if err != nil || address.Name != "" || address.Address != email {
		return false
	}
	if !utf8.ValidString(displayName) || len(displayName) > 320 || utf8.RuneCountInString(displayName) > 80 || strings.TrimSpace(displayName) != displayName {
		return false
	}
	for _, character := range displayName {
		if unicode.IsControl(character) {
			return false
		}
	}
	expiry, err := time.Parse(time.RFC3339, expires)
	return err == nil && !expiry.Before(now.Add(15*time.Minute)) && !expiry.After(now.Add(7*24*time.Hour))
}

func writeRemote(response http.ResponseWriter, status int, value any, err error) {
	if err != nil {
		switch {
		case errors.Is(err, authentikcontrol.ErrInvalid):
			writeError(response, http.StatusBadRequest, "invalid_request")
		case errors.Is(err, authentikcontrol.ErrAbsent):
			writeError(response, http.StatusNotFound, "absent")
		case errors.Is(err, authentikcontrol.ErrConflict):
			writeError(response, http.StatusConflict, "remote_conflict")
		case errors.Is(err, authentikcontrol.ErrRemoteInvalid):
			writeError(response, http.StatusBadGateway, "remote_invalid")
		default:
			writeError(response, http.StatusServiceUnavailable, "remote_unavailable")
		}
		return
	}
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}

func writeError(response http.ResponseWriter, status int, class string) {
	response.WriteHeader(status)
	_, _ = io.WriteString(response, `{"error":"`+class+`"}`+"\n")
}

type peerListener struct {
	*net.UnixListener
	peerUID uint32
}

func (listener *peerListener) Accept() (net.Conn, error) {
	for {
		connection, err := listener.AcceptUnix()
		if err != nil {
			return nil, err
		}
		allowed := false
		raw, err := connection.SyscallConn()
		if err == nil {
			err = raw.Control(func(descriptor uintptr) {
				credentials, credentialErr := unix.GetsockoptUcred(int(descriptor), unix.SOL_SOCKET, unix.SO_PEERCRED)
				allowed = credentialErr == nil && credentials.Uid == listener.peerUID
			})
		}
		if err == nil && allowed {
			return connection, nil
		}
		_ = connection.Close()
	}
}

type Listener struct {
	*peerListener
	path string
	dev  uint64
	ino  uint64
	once sync.Once
}

func Listen(path string) (*Listener, error) {
	return listen(path, gatewayUID, controlGID, boardUID)
}

func listen(path string, directoryUID, directoryGID, peerUID uint32) (*Listener, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || filepath.Base(path) != "authentik-control.sock" {
		return nil, fmt.Errorf("invalid gateway socket path")
	}
	parent, err := os.Lstat(filepath.Dir(path))
	if err != nil || !parent.IsDir() || parent.Mode().Perm() != 0o750 {
		return nil, fmt.Errorf("invalid gateway socket directory")
	}
	stat, ok := parent.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != directoryUID || stat.Gid != directoryGID {
		return nil, fmt.Errorf("invalid gateway socket directory")
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("gateway socket path is occupied")
	}
	base, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, fmt.Errorf("bind gateway socket")
	}
	base.SetUnlinkOnClose(false)
	if err := os.Chmod(path, 0o660); err != nil {
		_ = base.Close()
		_ = os.Remove(path)
		return nil, fmt.Errorf("secure gateway socket")
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != 0o660 {
		_ = base.Close()
		_ = os.Remove(path)
		return nil, fmt.Errorf("verify gateway socket")
	}
	socketStat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || socketStat.Uid != directoryUID || socketStat.Gid != directoryGID {
		_ = base.Close()
		_ = os.Remove(path)
		return nil, fmt.Errorf("verify gateway socket")
	}
	return &Listener{peerListener: &peerListener{UnixListener: base, peerUID: peerUID}, path: path, dev: uint64(socketStat.Dev), ino: socketStat.Ino}, nil
}

func (listener *Listener) Close() error {
	var result error
	listener.once.Do(func() {
		result = listener.UnixListener.Close()
		info, err := os.Lstat(listener.path)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) && result == nil {
				result = err
			}
			return
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if ok && uint64(stat.Dev) == listener.dev && stat.Ino == listener.ino {
			if err := os.Remove(listener.path); err != nil && result == nil {
				result = err
			}
		}
	})
	return result
}
