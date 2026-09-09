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
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

const clientTimeout = 2500 * time.Millisecond
const maximumResponse = 256 << 10

var (
	ErrInvalidRequest    = errors.New("invalid gateway request")
	ErrAbsentObject      = errors.New("gateway object absent")
	ErrRemoteConflict    = errors.New("gateway remote conflict")
	ErrRemoteInvalid     = errors.New("gateway remote invalid")
	ErrRemoteUnavailable = errors.New("gateway remote unavailable")
)

// Client is Board's closed HTTP-over-Unix control client. It has no token,
// configurable origin, path, method, or TCP fallback.
type Client struct {
	http *http.Client
}

func NewClient(socketPath string) (*Client, error) {
	if !filepath.IsAbs(socketPath) || filepath.Clean(socketPath) != socketPath || filepath.Base(socketPath) != "authentik-control.sock" {
		return nil, fmt.Errorf("invalid Authentik gateway socket")
	}
	dialer := &net.Dialer{Timeout: clientTimeout}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, "unix", socketPath)
		},
		DisableCompression:  true,
		DisableKeepAlives:   false,
		MaxIdleConns:        2,
		MaxIdleConnsPerHost: 2,
		IdleConnTimeout:     5 * time.Second,
	}
	return &Client{http: &http.Client{Transport: transport, Timeout: clientTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func (client *Client) Close() {
	if transport, ok := client.http.Transport.(*http.Transport); ok {
		transport.CloseIdleConnections()
	}
}

func (client *Client) User(ctx context.Context, uuid string) (User, error) {
	if !canonicalUUID.MatchString(uuid) {
		return User{}, ErrInvalidRequest
	}
	var result User
	err := client.request(ctx, http.MethodGet, "/v1/users/"+uuid, nil, http.StatusOK, &result)
	if err == nil && (!validGatewayUser(result) || result.UUID != uuid) {
		err = ErrRemoteInvalid
	}
	return result, err
}

func (client *Client) PendingUsers(ctx context.Context) ([]User, bool, error) {
	var result struct {
		Users []User `json:"users"`
		More  bool   `json:"more"`
	}
	err := client.request(ctx, http.MethodGet, "/v1/pending-users", nil, http.StatusOK, &result)
	if len(result.Users) > 51 {
		return nil, false, ErrRemoteInvalid
	}
	for _, user := range result.Users {
		if !validGatewayUser(user) {
			return nil, false, ErrRemoteInvalid
		}
	}
	return result.Users, result.More, err
}

func (client *Client) Groups(ctx context.Context) (map[string]Group, error) {
	var result struct {
		Accepted  Group `json:"accepted"`
		Pending   Group `json:"pending"`
		Suspended Group `json:"suspended"`
	}
	err := client.request(ctx, http.MethodGet, "/v1/groups", nil, http.StatusOK, &result)
	if err == nil && (!validGatewayGroup(result.Accepted) || !validGatewayGroup(result.Pending) || !validGatewayGroup(result.Suspended) || result.Accepted.UUID == result.Pending.UUID || result.Accepted.UUID == result.Suspended.UUID || result.Pending.UUID == result.Suspended.UUID) {
		err = ErrRemoteInvalid
	}
	return map[string]Group{"accepted": result.Accepted, "pending": result.Pending, "suspended": result.Suspended}, err
}

type Group struct {
	UUID string `json:"pk"`
	Name string `json:"name"`
}

func (client *Client) AddUser(ctx context.Context, group, userUUID string) error {
	return client.membership(ctx, group, "add", userUUID)
}

func (client *Client) RemoveUser(ctx context.Context, group, userUUID string) error {
	return client.membership(ctx, group, "remove", userUUID)
}

func (client *Client) membership(ctx context.Context, group, action, userUUID string) error {
	if (group != "accepted" && group != "pending" && group != "suspended") || !canonicalUUID.MatchString(userUUID) {
		return ErrInvalidRequest
	}
	body := struct {
		UserUUID string `json:"user_uuid"`
	}{userUUID}
	return client.request(ctx, http.MethodPost, "/v1/groups/"+group+"/"+action, body, http.StatusNoContent, nil)
}

func (client *Client) CreateInvitation(ctx context.Context, name, expires, email, displayName string) (Invitation, error) {
	body := struct {
		Name        string `json:"name"`
		Expires     string `json:"expires"`
		Email       string `json:"email"`
		DisplayName string `json:"display_name"`
	}{name, expires, email, displayName}
	var result Invitation
	err := client.request(ctx, http.MethodPost, "/v1/invitations", body, http.StatusCreated, &result)
	if err == nil && !validGatewayInvitation(result) {
		err = ErrRemoteInvalid
	}
	return result, err
}

func (client *Client) Invitations(ctx context.Context) ([]Invitation, bool, error) {
	var result struct {
		Invitations []Invitation `json:"invitations"`
		More        bool         `json:"more"`
	}
	err := client.request(ctx, http.MethodGet, "/v1/invitations", nil, http.StatusOK, &result)
	if len(result.Invitations) > 51 {
		return nil, false, ErrRemoteInvalid
	}
	for _, invitation := range result.Invitations {
		if !validGatewayInvitation(invitation) {
			return nil, false, ErrRemoteInvalid
		}
	}
	return result.Invitations, result.More, err
}

func (client *Client) Invitation(ctx context.Context, uuid string) (Invitation, error) {
	if !canonicalUUID.MatchString(uuid) {
		return Invitation{}, ErrInvalidRequest
	}
	var result Invitation
	err := client.request(ctx, http.MethodGet, "/v1/invitations/"+uuid, nil, http.StatusOK, &result)
	if err == nil && (!validGatewayInvitation(result) || result.UUID != uuid) {
		err = ErrRemoteInvalid
	}
	return result, err
}

func (client *Client) DeleteInvitation(ctx context.Context, uuid string) error {
	if !canonicalUUID.MatchString(uuid) {
		return ErrInvalidRequest
	}
	return client.request(ctx, http.MethodDelete, "/v1/invitations/"+uuid, nil, http.StatusNoContent, nil)
}

func (client *Client) Health(ctx context.Context) error {
	return client.request(ctx, http.MethodGet, "/health/live", nil, http.StatusNoContent, nil)
}

func (client *Client) request(ctx context.Context, method, path string, body any, expected int, destination any) error {
	if ctx == nil {
		return ErrInvalidRequest
	}
	var source io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil || len(raw) > maximumBody {
			return ErrInvalidRequest
		}
		source = bytes.NewReader(raw)
	}
	request, err := http.NewRequestWithContext(ctx, method, "http://"+fixedHost+path, source)
	if err != nil {
		return ErrInvalidRequest
	}
	request.Host = fixedHost
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.http.Do(request)
	if err != nil {
		return ErrRemoteUnavailable
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maximumResponse+1))
	if err != nil || len(raw) > maximumResponse {
		return ErrRemoteInvalid
	}
	if response.StatusCode != expected {
		if response.Header.Get("Content-Type") != "application/json" {
			return ErrRemoteInvalid
		}
		return decodeGatewayError(response.StatusCode, raw)
	}
	if destination == nil {
		if len(raw) != 0 || response.Header.Get("Content-Type") != "" {
			return ErrRemoteInvalid
		}
		return nil
	}
	if response.Header.Get("Content-Type") != "application/json" {
		return ErrRemoteInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil || duplicateObjectKey(raw) {
		return ErrRemoteInvalid
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return ErrRemoteInvalid
	}
	return nil
}

func decodeGatewayError(status int, raw []byte) error {
	values := map[int]struct {
		body string
		err  error
	}{
		http.StatusBadRequest:         {"{\"error\":\"invalid_request\"}\n", ErrInvalidRequest},
		http.StatusNotFound:           {"{\"error\":\"absent\"}\n", ErrAbsentObject},
		http.StatusConflict:           {"{\"error\":\"remote_conflict\"}\n", ErrRemoteConflict},
		http.StatusBadGateway:         {"{\"error\":\"remote_invalid\"}\n", ErrRemoteInvalid},
		http.StatusServiceUnavailable: {"{\"error\":\"remote_unavailable\"}\n", ErrRemoteUnavailable},
	}
	expected, ok := values[status]
	if !ok || string(raw) != expected.body {
		return ErrRemoteInvalid
	}
	return expected.err
}

func validLocalString(value string, maximumBytes, maximumRunes int) bool {
	return utf8.ValidString(value) && len(value) <= maximumBytes && utf8.RuneCountInString(value) <= maximumRunes && !strings.ContainsRune(value, 0)
}

func validGatewayUser(user User) bool {
	return user.ID > 0 && canonicalUUID.MatchString(user.UUID) && user.Username != "" && validLocalString(user.Username, 150, 150) && validLocalString(user.Name, 320, 80) && validLocalString(user.Email, 320, 320)
}

func validGatewayGroup(group Group) bool {
	return canonicalUUID.MatchString(group.UUID) && group.Name != "" && validLocalString(group.Name, 150, 150)
}

func validGatewayInvitation(invitation Invitation) bool {
	if !canonicalUUID.MatchString(invitation.UUID) || !canonicalSlug.MatchString(invitation.Name) || len(invitation.Name) > 150 || !invitation.SingleUse || !validLocalString(invitation.Email, 320, 320) || !validLocalString(invitation.DisplayName, 320, 80) {
		return false
	}
	_, err := time.Parse(time.RFC3339, invitation.Expires)
	return err == nil
}
