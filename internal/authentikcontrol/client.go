package authentikcontrol

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	ErrInvalid       = errors.New("invalid Authentik control operation")
	ErrAbsent        = errors.New("Authentik object absent")
	ErrConflict      = errors.New("Authentik object conflict")
	ErrRemoteInvalid = errors.New("invalid Authentik response")
	ErrUnavailable   = errors.New("Authentik unavailable")
)

const (
	requestTimeout       = 2 * time.Second
	maximumResponseBytes = 256 << 10
	maximumDrainBytes    = 4 << 10
	maximumPageSize      = 51
)

// User is the bounded identity projection required for group transitions.
type User struct {
	PK       int64  `json:"pk"`
	UUID     string `json:"uuid"`
	Username string `json:"username"`
	Name     string `json:"name"`
	Email    string `json:"email"`
	Active   bool   `json:"is_active"`
}

// Group is the exact immutable group projection.
type Group struct {
	UUID string `json:"pk"`
	Name string `json:"name"`
}

// Invitation is the bounded invitation projection. Its UUID is bearer material
// and must never be formatted or logged by this package.
type Invitation struct {
	UUID      string         `json:"pk"`
	Name      string         `json:"name"`
	Expires   string         `json:"expires"`
	FixedData map[string]any `json:"fixed_data"`
	SingleUse bool           `json:"single_use"`
	Flow      string         `json:"flow"`
}

type page[T any] struct {
	Pagination struct {
		Next int `json:"next"`
	} `json:"pagination"`
	Results []T `json:"results"`
}

// Client exposes only the admitted fixed-origin operations.
type Client struct {
	origin  *url.URL
	token   Secret
	objects Objects
	http    *http.Client
}

// New constructs a no-redirect, system-TLS client for the issuer origin.
func New(issuer string, token Secret, objects Objects) (*Client, error) {
	parsed, err := url.Parse(issuer)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || len(token.bytes) == 0 {
		token.destroy()
		return nil, fmt.Errorf("Authentik control configuration is invalid")
	}
	origin := &url.URL{Scheme: parsed.Scheme, Host: parsed.Host}
	if objects.IssuerOrigin != origin.String() {
		token.destroy()
		return nil, fmt.Errorf("Authentik control configuration is invalid")
	}
	return &Client{origin: origin, token: token, objects: objects, http: &http.Client{
		Timeout:       requestTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

// Close destroys the client's owned token bytes.
func (client *Client) Close() { client.token.destroy() }

// UserByUUID retrieves exactly one user through Authentik's pinned UUID filter.
func (client *Client) UserByUUID(ctx context.Context, uuid string) (User, bool, error) {
	if !canonicalUUID.MatchString(uuid) {
		return User{}, false, ErrInvalid
	}
	query := url.Values{"uuid": {uuid}, "include_groups": {"false"}, "include_roles": {"false"}, "page_size": {"2"}}
	var response page[User]
	if err := client.requestJSON(ctx, http.MethodGet, "/api/v3/core/users/", query, nil, http.StatusOK, &response); err != nil {
		return User{}, false, err
	}
	if len(response.Results) == 0 {
		return User{}, false, nil
	}
	if len(response.Results) != 1 || response.Pagination.Next != 0 || response.Results[0].UUID != uuid || !validUser(response.Results[0]) {
		return User{}, false, ErrRemoteInvalid
	}
	return response.Results[0], true, nil
}

// PendingUsers lists only the first bounded page of the pinned pending group.
func (client *Client) PendingUsers(ctx context.Context) ([]User, bool, error) {
	query := url.Values{"groups_by_pk": {client.objects.Groups.Pending}, "include_groups": {"false"}, "include_roles": {"false"}, "page_size": {strconv.Itoa(maximumPageSize)}}
	var response page[User]
	if err := client.requestJSON(ctx, http.MethodGet, "/api/v3/core/users/", query, nil, http.StatusOK, &response); err != nil {
		return nil, false, err
	}
	if len(response.Results) > maximumPageSize {
		return nil, false, ErrRemoteInvalid
	}
	for _, user := range response.Results {
		if !validUser(user) {
			return nil, false, ErrRemoteInvalid
		}
	}
	return response.Results, response.Pagination.Next != 0, nil
}

// UserInGroup verifies one exact UUID against one pinned group without
// returning or accepting a mutable membership set.
func (client *Client) UserInGroup(ctx context.Context, userUUID, groupUUID string) (bool, error) {
	if !canonicalUUID.MatchString(userUUID) || !client.pinnedGroup(groupUUID) {
		return false, ErrInvalid
	}
	query := url.Values{
		"uuid":           {userUUID},
		"groups_by_pk":   {groupUUID},
		"include_groups": {"false"},
		"include_roles":  {"false"},
		"page_size":      {"2"},
	}
	var response page[User]
	if err := client.requestJSON(ctx, http.MethodGet, "/api/v3/core/users/", query, nil, http.StatusOK, &response); err != nil {
		return false, err
	}
	if len(response.Results) == 0 {
		return false, nil
	}
	if len(response.Results) != 1 || response.Pagination.Next != 0 || response.Results[0].UUID != userUUID || !validUser(response.Results[0]) {
		return false, ErrRemoteInvalid
	}
	return true, nil
}

// Groups retrieves and verifies all three pinned groups.
func (client *Client) Groups(ctx context.Context) (Group, Group, Group, error) {
	accepted, err := client.group(ctx, client.objects.Groups.Accepted)
	if err != nil {
		return Group{}, Group{}, Group{}, err
	}
	pending, err := client.group(ctx, client.objects.Groups.Pending)
	if err != nil {
		return Group{}, Group{}, Group{}, err
	}
	suspended, err := client.group(ctx, client.objects.Groups.Suspended)
	if err != nil {
		return Group{}, Group{}, Group{}, err
	}
	return accepted, pending, suspended, nil
}

func (client *Client) group(ctx context.Context, uuid string) (Group, error) {
	var group Group
	if err := client.requestJSON(ctx, http.MethodGet, "/api/v3/core/groups/"+uuid+"/", url.Values{"include_users": {"false"}}, nil, http.StatusOK, &group); err != nil {
		return Group{}, err
	}
	if group.UUID != uuid || group.Name == "" || len(group.Name) > 150 || !utf8.ValidString(group.Name) {
		return Group{}, ErrRemoteInvalid
	}
	return group, nil
}

func validUser(user User) bool {
	return user.PK > 0 && canonicalUUID.MatchString(user.UUID) && user.Username != "" && len(user.Username) <= 150 && utf8.ValidString(user.Username) && len(user.Name) <= 320 && utf8.ValidString(user.Name) && len(user.Email) <= 320 && utf8.ValidString(user.Email)
}

// AddUser adds one resolved numeric user to one of the three pinned groups.
func (client *Client) AddUser(ctx context.Context, groupUUID string, userPK int64) error {
	return client.membership(ctx, groupUUID, userPK, "add_user")
}

// RemoveUser removes one resolved numeric user from one of the pinned groups.
func (client *Client) RemoveUser(ctx context.Context, groupUUID string, userPK int64) error {
	return client.membership(ctx, groupUUID, userPK, "remove_user")
}

func (client *Client) membership(ctx context.Context, groupUUID string, userPK int64, action string) error {
	if !client.pinnedGroup(groupUUID) || userPK <= 0 {
		return ErrInvalid
	}
	body := struct {
		PK int64 `json:"pk"`
	}{PK: userPK}
	return client.requestJSON(ctx, http.MethodPost, "/api/v3/core/groups/"+groupUUID+"/"+action+"/", nil, body, http.StatusNoContent, nil)
}

func (client *Client) pinnedGroup(uuid string) bool {
	return uuid == client.objects.Groups.Accepted || uuid == client.objects.Groups.Pending || uuid == client.objects.Groups.Suspended
}

// CreateInvitation creates one single-use invitation forced to the pinned flow.
func (client *Client) CreateInvitation(ctx context.Context, name, expires, email, displayName string) (Invitation, error) {
	if !canonicalSlug.MatchString(name) || name == "" || len(name) > 150 || email == "" || len(email) > 320 {
		return Invitation{}, ErrInvalid
	}
	fixedData := map[string]string{"email": email}
	if displayName != "" {
		fixedData["name"] = displayName
	}
	body := struct {
		Name      string            `json:"name"`
		Expires   string            `json:"expires"`
		FixedData map[string]string `json:"fixed_data"`
		SingleUse bool              `json:"single_use"`
		Flow      string            `json:"flow"`
	}{name, expires, fixedData, true, client.objects.Flows.Invitation.UUID}
	var invitation Invitation
	if err := client.requestJSON(ctx, http.MethodPost, "/api/v3/stages/invitation/invitations/", nil, body, http.StatusCreated, &invitation); err != nil {
		return Invitation{}, err
	}
	if err := client.verifyInvitation(invitation); err != nil {
		return Invitation{}, err
	}
	returnedEmail, _ := invitation.FixedData["email"].(string)
	returnedName, _ := invitation.FixedData["name"].(string)
	wantedExpiry, wantedErr := time.Parse(time.RFC3339, expires)
	returnedExpiry, returnedErr := time.Parse(time.RFC3339, invitation.Expires)
	if wantedErr != nil || returnedErr != nil || !wantedExpiry.Equal(returnedExpiry) || invitation.Name != name || returnedEmail != email || returnedName != displayName {
		return Invitation{}, ErrRemoteInvalid
	}
	return invitation, nil
}

// Invitations lists at most 51 invitations forced to the pinned flow slug.
func (client *Client) Invitations(ctx context.Context) ([]Invitation, bool, error) {
	query := url.Values{"flow__slug": {client.objects.Flows.Invitation.Slug}, "page_size": {strconv.Itoa(maximumPageSize)}}
	var response page[Invitation]
	if err := client.requestJSON(ctx, http.MethodGet, "/api/v3/stages/invitation/invitations/", query, nil, http.StatusOK, &response); err != nil {
		return nil, false, err
	}
	if len(response.Results) > maximumPageSize {
		return nil, false, ErrRemoteInvalid
	}
	for _, invitation := range response.Results {
		if err := client.verifyInvitation(invitation); err != nil {
			return nil, false, err
		}
	}
	return response.Results, response.Pagination.Next != 0, nil
}

// Invitation retrieves a caller-selected UUID only within the pinned flow.
func (client *Client) Invitation(ctx context.Context, uuid string) (Invitation, error) {
	if !canonicalUUID.MatchString(uuid) {
		return Invitation{}, ErrInvalid
	}
	var invitation Invitation
	if err := client.requestJSON(ctx, http.MethodGet, "/api/v3/stages/invitation/invitations/"+uuid+"/", nil, nil, http.StatusOK, &invitation); err != nil {
		return Invitation{}, err
	}
	if invitation.UUID != uuid {
		return Invitation{}, ErrRemoteInvalid
	}
	if err := client.verifyInvitation(invitation); err != nil {
		return Invitation{}, err
	}
	return invitation, nil
}

// DeleteInvitation deletes a verified invitation from the pinned flow.
func (client *Client) DeleteInvitation(ctx context.Context, uuid string) error {
	if _, err := client.Invitation(ctx, uuid); err != nil {
		return err
	}
	return client.requestJSON(ctx, http.MethodDelete, "/api/v3/stages/invitation/invitations/"+uuid+"/", nil, nil, http.StatusNoContent, nil)
}

func (client *Client) verifyInvitation(invitation Invitation) error {
	if !canonicalUUID.MatchString(invitation.UUID) || invitation.Flow != client.objects.Flows.Invitation.UUID || !invitation.SingleUse || !canonicalSlug.MatchString(invitation.Name) || len(invitation.Name) > 150 {
		return ErrRemoteInvalid
	}
	if _, err := time.Parse(time.RFC3339, invitation.Expires); err != nil {
		return ErrRemoteInvalid
	}
	if len(invitation.FixedData) < 1 || len(invitation.FixedData) > 2 {
		return ErrRemoteInvalid
	}
	email, ok := invitation.FixedData["email"].(string)
	if !ok || email == "" || len(email) > 320 || !utf8.ValidString(email) || strings.ContainsAny(email, "\r\n\x00") {
		return ErrRemoteInvalid
	}
	if name, present := invitation.FixedData["name"]; present {
		value, ok := name.(string)
		if !ok || len(value) > 320 || !utf8.ValidString(value) {
			return ErrRemoteInvalid
		}
	}
	return nil
}

func (client *Client) requestJSON(ctx context.Context, method, path string, query url.Values, body any, expected int, destination any) error {
	if ctx == nil {
		return ErrInvalid
	}
	endpoint := *client.origin
	endpoint.Path = path
	endpoint.RawQuery = query.Encode()
	var source io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return ErrInvalid
		}
		source = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint.String(), source)
	if err != nil {
		return ErrInvalid
	}
	request.Header.Set("Authorization", "Bearer "+string(client.token.bytes))
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.http.Do(request)
	if err != nil {
		return ErrUnavailable
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maximumDrainBytes))
		_ = response.Body.Close()
	}()
	if response.StatusCode != expected {
		switch response.StatusCode {
		case http.StatusNotFound:
			return ErrAbsent
		case http.StatusConflict:
			return ErrConflict
		default:
			return ErrRemoteInvalid
		}
	}
	if destination == nil {
		var one [1]byte
		if count, readErr := response.Body.Read(one[:]); count != 0 || readErr != io.EOF {
			return ErrRemoteInvalid
		}
		return nil
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maximumResponseBytes+1))
	if err != nil || len(raw) > maximumResponseBytes {
		return ErrRemoteInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(destination); err != nil {
		return ErrRemoteInvalid
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return ErrRemoteInvalid
	}
	return nil
}
