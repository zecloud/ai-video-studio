package backend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/zecloud/ai-video-studio/internal/onedrive"
	"github.com/zecloud/ai-video-studio/internal/settings"
)

var errOneDriveTokenNotCached = errors.New("OneDrive token is not cached")

type oneDriveTokenStore interface {
	Load(context.Context, onedrive.GraphAuthConfig) (onedrive.TokenSet, error)
	Save(context.Context, onedrive.GraphAuthConfig, onedrive.TokenSet) error
	Delete(context.Context) error
	Available() bool
	Description() string
}

type oneDriveTokenCache struct {
	path      string
	protector tokenProtector
}

type tokenProtector interface {
	Protect([]byte) ([]byte, error)
	Unprotect([]byte) ([]byte, error)
	Description() string
}

type storedOneDriveToken struct {
	TenantID     string    `json:"tenantId"`
	ClientID     string    `json:"clientId"`
	Scopes       []string  `json:"scopes"`
	TokenType    string    `json:"tokenType"`
	AccessToken  string    `json:"accessToken"`
	RefreshToken string    `json:"refreshToken"`
	ExpiresAt    time.Time `json:"expiresAt"`
}

func newDefaultOneDriveTokenStore() oneDriveTokenStore {
	path, err := defaultOneDriveTokenPath()
	if err != nil {
		return noopOneDriveTokenStore{reason: err.Error()}
	}
	protector := newPlatformTokenProtector()
	if protector == nil {
		return noopOneDriveTokenStore{reason: "secure OS token protection is unavailable on this platform"}
	}
	return &oneDriveTokenCache{path: path, protector: protector}
}

func defaultOneDriveTokenPath() (string, error) {
	settingsPath, err := settings.DefaultPath()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(settingsPath), "onedrive-token.dpapi"), nil
}

func (s *oneDriveTokenCache) Load(_ context.Context, cfg onedrive.GraphAuthConfig) (onedrive.TokenSet, error) {
	if s == nil || s.path == "" || s.protector == nil {
		return onedrive.TokenSet{}, errOneDriveTokenNotCached
	}
	protected, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return onedrive.TokenSet{}, errOneDriveTokenNotCached
	}
	if err != nil {
		return onedrive.TokenSet{}, err
	}
	plain, err := s.protector.Unprotect(protected)
	if err != nil {
		return onedrive.TokenSet{}, fmt.Errorf("unprotect OneDrive token cache: %w", err)
	}
	var stored storedOneDriveToken
	if err := json.Unmarshal(plain, &stored); err != nil {
		return onedrive.TokenSet{}, fmt.Errorf("read OneDrive token cache: %w", err)
	}
	if !stored.matches(cfg) {
		return onedrive.TokenSet{}, errOneDriveTokenNotCached
	}
	if strings.TrimSpace(stored.RefreshToken) == "" {
		return onedrive.TokenSet{}, errOneDriveTokenNotCached
	}
	return onedrive.TokenSet{
		AccessToken:  stored.AccessToken,
		RefreshToken: stored.RefreshToken,
		TokenType:    stored.TokenType,
		Scopes:       append([]string(nil), stored.Scopes...),
		ExpiresAt:    stored.ExpiresAt,
	}, nil
}

func (s *oneDriveTokenCache) Save(_ context.Context, cfg onedrive.GraphAuthConfig, token onedrive.TokenSet) error {
	if s == nil || s.path == "" || s.protector == nil {
		return errOneDriveTokenNotCached
	}
	if strings.TrimSpace(token.RefreshToken) == "" {
		return fmt.Errorf("%w: refresh token is empty", onedrive.ErrAuthConfigIncomplete)
	}
	stored := storedOneDriveToken{
		TenantID:     strings.TrimSpace(cfg.TenantID),
		ClientID:     strings.TrimSpace(cfg.ClientID),
		Scopes:       append([]string(nil), token.Scopes...),
		TokenType:    token.TokenType,
		AccessToken:  token.AccessToken,
		RefreshToken: token.RefreshToken,
		ExpiresAt:    token.ExpiresAt,
	}
	if len(stored.Scopes) == 0 {
		stored.Scopes = append([]string(nil), cfg.Scopes...)
	}
	data, err := json.Marshal(stored)
	if err != nil {
		return err
	}
	protected, err := s.protector.Protect(data)
	if err != nil {
		return fmt.Errorf("protect OneDrive token cache: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(s.path, protected, 0o600)
}

func (s *oneDriveTokenCache) Delete(_ context.Context) error {
	if s == nil || s.path == "" {
		return nil
	}
	err := os.Remove(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (s *oneDriveTokenCache) Available() bool {
	return s != nil && s.path != "" && s.protector != nil
}

func (s *oneDriveTokenCache) Description() string {
	if s == nil || s.protector == nil {
		return "No secure token cache"
	}
	return s.protector.Description()
}

func (t storedOneDriveToken) matches(cfg onedrive.GraphAuthConfig) bool {
	if strings.TrimSpace(t.TenantID) != strings.TrimSpace(cfg.TenantID) {
		return false
	}
	if strings.TrimSpace(t.ClientID) != strings.TrimSpace(cfg.ClientID) {
		return false
	}
	return scopeSetContains(t.Scopes, cfg.Scopes)
}

func scopeSetContains(available, required []string) bool {
	clean := func(scopes []string) []string {
		values := make([]string, 0, len(scopes))
		seen := map[string]struct{}{}
		for _, scope := range scopes {
			scope = strings.TrimSpace(scope)
			if scope == "" {
				continue
			}
			if _, ok := seen[scope]; ok {
				continue
			}
			seen[scope] = struct{}{}
			values = append(values, scope)
		}
		slices.Sort(values)
		return values
	}
	availableScopes := clean(available)
	requiredScopes := clean(required)
	if len(requiredScopes) == 0 {
		return true
	}
	for _, requiredScope := range requiredScopes {
		if _, ok := slices.BinarySearch(availableScopes, requiredScope); !ok {
			return false
		}
	}
	return true
}

type noopOneDriveTokenStore struct {
	reason string
}

func (s noopOneDriveTokenStore) Load(context.Context, onedrive.GraphAuthConfig) (onedrive.TokenSet, error) {
	return onedrive.TokenSet{}, errOneDriveTokenNotCached
}

func (s noopOneDriveTokenStore) Save(context.Context, onedrive.GraphAuthConfig, onedrive.TokenSet) error {
	return errOneDriveTokenNotCached
}

func (s noopOneDriveTokenStore) Delete(context.Context) error {
	return nil
}

func (s noopOneDriveTokenStore) Available() bool {
	return false
}

func (s noopOneDriveTokenStore) Description() string {
	if strings.TrimSpace(s.reason) == "" {
		return "Secure token cache unavailable"
	}
	return s.reason
}
