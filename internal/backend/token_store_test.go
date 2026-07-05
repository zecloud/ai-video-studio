package backend

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zecloud/ai-video-studio/internal/onedrive"
	"github.com/zecloud/ai-video-studio/internal/settings"
)

func TestOneDriveTokenCacheRoundTrip(t *testing.T) {
	ctx := context.Background()
	cfg := onedrive.GraphAuthConfig{
		TenantID: "organizations",
		ClientID: "client-id",
		Scopes:   []string{onedrive.GraphScopeFilesReadWriteAppFolder},
	}
	cache := &oneDriveTokenCache{
		path:      filepath.Join(t.TempDir(), "onedrive-token.dpapi"),
		protector: testTokenProtector{},
	}
	expiresAt := time.Date(2026, 7, 5, 13, 0, 0, 0, time.UTC)
	token := onedrive.TokenSet{
		AccessToken:  "access-token",
		RefreshToken: "refresh-token",
		TokenType:    "Bearer",
		Scopes:       append([]string(nil), cfg.Scopes...),
		ExpiresAt:    expiresAt,
	}

	if err := cache.Save(ctx, cfg, token); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	raw, err := os.ReadFile(cache.path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if bytes.Contains(raw, []byte("refresh-token")) {
		t.Fatalf("token cache contains the plain refresh token")
	}

	got, err := cache.Load(ctx, cfg)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.AccessToken != token.AccessToken || got.RefreshToken != token.RefreshToken || !got.ExpiresAt.Equal(expiresAt) {
		t.Fatalf("Load() = %#v, want %#v", got, token)
	}

	otherCfg := cfg
	otherCfg.ClientID = "other-client"
	if _, err := cache.Load(ctx, otherCfg); !errors.Is(err, errOneDriveTokenNotCached) {
		t.Fatalf("Load(other client) error = %v, want cache miss", err)
	}
}

func TestOneDriveTokenCacheAcceptsSupersetScopes(t *testing.T) {
	ctx := context.Background()
	cfg := onedrive.GraphAuthConfig{
		TenantID: "organizations",
		ClientID: "client-id",
		Scopes:   []string{onedrive.GraphScopeFilesReadWriteAppFolder},
	}
	cache := &oneDriveTokenCache{
		path:      filepath.Join(t.TempDir(), "onedrive-token.dpapi"),
		protector: testTokenProtector{},
	}
	token := onedrive.TokenSet{
		AccessToken:  "access-token",
		RefreshToken: "refresh-token",
		TokenType:    "Bearer",
		Scopes:       []string{onedrive.GraphScopeFilesReadWriteAppFolder, onedrive.OfflineAccessScope},
		ExpiresAt:    time.Date(2026, 7, 5, 13, 0, 0, 0, time.UTC),
	}

	if err := cache.Save(ctx, cfg, token); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if _, err := cache.Load(ctx, cfg); err != nil {
		t.Fatalf("Load() error = %v, want cached token with superset scopes accepted", err)
	}
}

func TestOneDriveServiceAccessTokenRefreshesCachedToken(t *testing.T) {
	ctx := context.Background()
	cfg := defaultSettings()
	cfg.GraphAuth.ClientID = "client-id"
	cfg.GraphAuth.TenantID = "organizations"
	cfg.GraphAuth.Scopes = []string{onedrive.GraphScopeFilesReadWriteAppFolder}

	var refreshRequest url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm() error = %v", err)
		}
		refreshRequest = r.PostForm
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"token_type":"Bearer","scope":"Files.ReadWrite.AppFolder","expires_in":3600,"access_token":"fresh-access","refresh_token":"fresh-refresh"}`))
	}))
	defer server.Close()

	tokenStore := &testOneDriveTokenStore{
		available: true,
		token: onedrive.TokenSet{
			RefreshToken: "cached-refresh",
			TokenType:    "Bearer",
			Scopes:       append([]string(nil), cfg.GraphAuth.Scopes...),
			ExpiresAt:    time.Now().Add(-time.Hour),
		},
	}
	service := &OneDriveService{
		store:      testSettingsStore{settings: cfg},
		tokenStore: tokenStore,
		authClient: &onedrive.AuthClient{
			HTTPClient:       server.Client(),
			AuthorityBaseURL: server.URL,
			Now:              func() time.Time { return time.Date(2026, 7, 5, 13, 0, 0, 0, time.UTC) },
		},
	}

	accessToken, err := service.accessToken(ctx, cfg.GraphAuth.Scopes)
	if err != nil {
		t.Fatalf("accessToken() error = %v", err)
	}
	if accessToken != "fresh-access" {
		t.Fatalf("accessToken() = %q, want fresh-access", accessToken)
	}
	if refreshRequest.Get("refresh_token") != "cached-refresh" {
		t.Fatalf("refresh token form = %q, want cached-refresh", refreshRequest.Get("refresh_token"))
	}
	if tokenStore.saved.RefreshToken != "fresh-refresh" {
		t.Fatalf("saved refresh token = %q, want fresh-refresh", tokenStore.saved.RefreshToken)
	}
}

func TestOneDriveServiceAccessTokenRequiresSignInWhenCacheMissing(t *testing.T) {
	ctx := context.Background()
	cfg := defaultSettings()
	cfg.GraphAuth.ClientID = "client-id"
	cfg.GraphAuth.TenantID = "organizations"
	cfg.GraphAuth.Scopes = []string{onedrive.GraphScopeFilesReadWriteAppFolder}
	service := &OneDriveService{
		store:      testSettingsStore{settings: cfg},
		tokenStore: &testOneDriveTokenStore{available: true},
		authClient: onedrive.NewAuthClient(nil),
	}

	_, err := service.accessToken(ctx, cfg.GraphAuth.Scopes)
	if !errors.Is(err, errOneDriveSignInRequired) {
		t.Fatalf("accessToken() error = %v, want errOneDriveSignInRequired", err)
	}
	if errors.Is(err, errOneDriveTokenNotCached) {
		t.Fatalf("accessToken() leaked internal cache miss error: %v", err)
	}
}

type testTokenProtector struct{}

func (testTokenProtector) Protect(data []byte) ([]byte, error) {
	out := append([]byte("protected:"), data...)
	for i := len("protected:"); i < len(out); i++ {
		out[i] ^= 0x5a
	}
	return out, nil
}

func (testTokenProtector) Unprotect(data []byte) ([]byte, error) {
	if !bytes.HasPrefix(data, []byte("protected:")) {
		return nil, errors.New("missing test protection prefix")
	}
	out := append([]byte(nil), data[len("protected:"):]...)
	for i := range out {
		out[i] ^= 0x5a
	}
	return out, nil
}

func (testTokenProtector) Description() string { return "test protector" }

type testOneDriveTokenStore struct {
	available bool
	token     onedrive.TokenSet
	saved     onedrive.TokenSet
	deleted   bool
}

func (s *testOneDriveTokenStore) Load(context.Context, onedrive.GraphAuthConfig) (onedrive.TokenSet, error) {
	if !s.available || strings.TrimSpace(s.token.RefreshToken) == "" {
		return onedrive.TokenSet{}, errOneDriveTokenNotCached
	}
	return s.token, nil
}

func (s *testOneDriveTokenStore) Save(_ context.Context, _ onedrive.GraphAuthConfig, token onedrive.TokenSet) error {
	s.saved = token
	return nil
}

func (s *testOneDriveTokenStore) Delete(context.Context) error {
	s.deleted = true
	return nil
}

func (s *testOneDriveTokenStore) Available() bool { return s.available }

func (s *testOneDriveTokenStore) Description() string { return "test token store" }

type testSettingsStore struct {
	settings settings.AppSettings
}

func (s testSettingsStore) Load(context.Context) (settings.AppSettings, error) {
	return s.settings, nil
}

func (s testSettingsStore) Save(_ context.Context, next settings.AppSettings) (settings.AppSettings, error) {
	return next, nil
}

func (s testSettingsStore) Path() string { return "" }
