package onedrive

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAuthClientStartDeviceCodePostsExpectedForm(t *testing.T) {
	now := time.Date(2026, 7, 4, 15, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/organizations/oauth2/v2.0/devicecode" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parse form: %v", err)
		}
		if r.Form.Get("client_id") != "client-1" {
			t.Fatalf("client_id = %q", r.Form.Get("client_id"))
		}
		scope := r.Form.Get("scope")
		if !strings.Contains(scope, GraphScopeFilesReadWriteAppFolder) || !strings.Contains(scope, OfflineAccessScope) {
			t.Fatalf("scope did not include Graph and offline scopes: %q", scope)
		}
		_, _ = io.WriteString(w, `{"device_code":"device-1","user_code":"ABCD-EFGH","verification_uri":"https://microsoft.com/devicelogin","expires_in":900,"interval":5,"message":"Use code ABCD-EFGH"}`)
	}))
	defer server.Close()

	client := NewAuthClient(server.Client())
	client.AuthorityBaseURL = server.URL
	client.Now = func() time.Time { return now }

	session, err := client.StartDeviceCode(context.Background(), GraphAuthConfig{ClientID: "client-1", AuthFlow: AuthFlowDeviceCode, Scopes: DefaultGraphScopes})
	if err != nil {
		t.Fatalf("StartDeviceCode returned error: %v", err)
	}
	if session.DeviceCode != "device-1" || session.UserCode != "ABCD-EFGH" || session.IntervalSeconds != 5 {
		t.Fatalf("unexpected session: %+v", session)
	}
	if !session.ExpiresAt.Equal(now.Add(15 * time.Minute)) {
		t.Fatalf("unexpected expiration: %s", session.ExpiresAt)
	}
}

func TestAuthClientPollDeviceCodeHandlesPendingAndSuccess(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/organizations/oauth2/v2.0/token" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parse form: %v", err)
		}
		if r.Form.Get("grant_type") != DeviceCodeGrantType || r.Form.Get("device_code") != "device-1" {
			t.Fatalf("unexpected token form: %v", r.Form)
		}
		if calls == 1 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":"authorization_pending"}`)
			return
		}
		_, _ = io.WriteString(w, `{"token_type":"Bearer","scope":"Files.ReadWrite.AppFolder offline_access","expires_in":3600,"access_token":"access-1","refresh_token":"refresh-1"}`)
	}))
	defer server.Close()

	client := NewAuthClient(server.Client())
	client.AuthorityBaseURL = server.URL
	session := DeviceCodeSession{
		DeviceCode: "device-1",
		TenantID:   "organizations",
		ClientID:   "client-1",
		ExpiresAt:  time.Now().Add(time.Minute),
	}

	_, err := client.PollDeviceCode(context.Background(), session)
	if !errors.Is(err, ErrAuthorizationPending) {
		t.Fatalf("expected pending error, got %v", err)
	}
	token, err := client.PollDeviceCode(context.Background(), session)
	if err != nil {
		t.Fatalf("PollDeviceCode returned error: %v", err)
	}
	if token.AccessToken != "access-1" || token.RefreshToken != "refresh-1" || len(token.Scopes) != 2 {
		t.Fatalf("unexpected token: %+v", token)
	}
}

func TestAuthClientRejectsMissingClientID(t *testing.T) {
	_, err := NewAuthClient(nil).StartDeviceCode(context.Background(), GraphAuthConfig{})
	if !errors.Is(err, ErrAuthConfigIncomplete) {
		t.Fatalf("expected ErrAuthConfigIncomplete, got %v", err)
	}
}
