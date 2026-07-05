package onedrive

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	DefaultAuthorityBaseURL = "https://login.microsoftonline.com"
	DefaultTenantID         = "organizations"
	OfflineAccessScope      = "offline_access"
	DeviceCodeGrantType     = "urn:ietf:params:oauth:grant-type:device_code"
	RefreshTokenGrantType   = "refresh_token"
)

var (
	ErrAuthConfigIncomplete = errors.New("Microsoft Graph auth configuration is incomplete")
	ErrDeviceCodeExpired    = errors.New("Microsoft Graph device code expired")
	ErrAuthorizationPending = errors.New("Microsoft Graph authorization is pending")
	ErrAuthorizationDenied  = errors.New("Microsoft Graph authorization was denied")
	ErrAuthFailed           = errors.New("Microsoft Graph auth failed")
)

type AuthClient struct {
	HTTPClient       HTTPClient
	AuthorityBaseURL string
	Now              func() time.Time
}

type DeviceCodeSession struct {
	DeviceCode      string    `json:"-"`
	UserCode        string    `json:"userCode"`
	VerificationURI string    `json:"verificationUri"`
	Message         string    `json:"message"`
	ExpiresAt       time.Time `json:"expiresAt"`
	IntervalSeconds int       `json:"intervalSeconds"`
	TenantID        string    `json:"tenantId"`
	ClientID        string    `json:"clientId"`
	Scopes          []string  `json:"scopes"`
}

type TokenSet struct {
	AccessToken  string    `json:"-"`
	RefreshToken string    `json:"-"`
	TokenType    string    `json:"tokenType"`
	Scopes       []string  `json:"scopes"`
	ExpiresAt    time.Time `json:"expiresAt"`
}

type authErrorResponse struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

type deviceCodeResponse struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
	Message         string `json:"message"`
}

type tokenResponse struct {
	TokenType    string `json:"token_type"`
	Scope        string `json:"scope"`
	ExpiresIn    int    `json:"expires_in"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
}

func NewAuthClient(httpClient HTTPClient) *AuthClient {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &AuthClient{HTTPClient: httpClient}
}

func (c *AuthClient) StartDeviceCode(ctx context.Context, cfg GraphAuthConfig) (DeviceCodeSession, error) {
	cfg = normalizeAuthConfig(cfg)
	if err := validateAuthConfig(cfg); err != nil {
		return DeviceCodeSession{}, err
	}
	values := url.Values{}
	values.Set("client_id", cfg.ClientID)
	values.Set("scope", strings.Join(authScopes(cfg.Scopes), " "))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint(cfg.TenantID, "devicecode"), strings.NewReader(values.Encode()))
	if err != nil {
		return DeviceCodeSession{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	res, err := c.httpClient().Do(req)
	if err != nil {
		return DeviceCodeSession{}, err
	}
	defer res.Body.Close()
	if res.StatusCode < http.StatusOK || res.StatusCode >= http.StatusMultipleChoices {
		return DeviceCodeSession{}, parseAuthError(res.Body, res.Status)
	}

	var parsed deviceCodeResponse
	if err := json.NewDecoder(res.Body).Decode(&parsed); err != nil {
		return DeviceCodeSession{}, fmt.Errorf("%w: invalid device-code response: %v", ErrAuthFailed, err)
	}
	if strings.TrimSpace(parsed.DeviceCode) == "" || strings.TrimSpace(parsed.UserCode) == "" || strings.TrimSpace(parsed.VerificationURI) == "" {
		return DeviceCodeSession{}, fmt.Errorf("%w: device-code response is missing required fields", ErrAuthFailed)
	}
	if parsed.Interval <= 0 {
		parsed.Interval = 5
	}
	now := c.now()
	return DeviceCodeSession{
		DeviceCode:      parsed.DeviceCode,
		UserCode:        parsed.UserCode,
		VerificationURI: parsed.VerificationURI,
		Message:         parsed.Message,
		ExpiresAt:       now.Add(time.Duration(parsed.ExpiresIn) * time.Second),
		IntervalSeconds: parsed.Interval,
		TenantID:        cfg.TenantID,
		ClientID:        cfg.ClientID,
		Scopes:          authScopes(cfg.Scopes),
	}, nil
}

func (c *AuthClient) PollDeviceCode(ctx context.Context, session DeviceCodeSession) (TokenSet, error) {
	if strings.TrimSpace(session.DeviceCode) == "" || strings.TrimSpace(session.ClientID) == "" {
		return TokenSet{}, ErrAuthConfigIncomplete
	}
	if !session.ExpiresAt.IsZero() && !c.now().Before(session.ExpiresAt) {
		return TokenSet{}, ErrDeviceCodeExpired
	}
	tenant := strings.TrimSpace(session.TenantID)
	if tenant == "" {
		tenant = DefaultTenantID
	}
	values := url.Values{}
	values.Set("grant_type", DeviceCodeGrantType)
	values.Set("client_id", session.ClientID)
	values.Set("device_code", session.DeviceCode)
	return c.requestToken(ctx, tenant, values)
}

func (c *AuthClient) Refresh(ctx context.Context, cfg GraphAuthConfig, refreshToken string) (TokenSet, error) {
	cfg = normalizeAuthConfig(cfg)
	if err := validateAuthConfig(cfg); err != nil {
		return TokenSet{}, err
	}
	if strings.TrimSpace(refreshToken) == "" {
		return TokenSet{}, fmt.Errorf("%w: refresh token is empty", ErrAuthConfigIncomplete)
	}
	values := url.Values{}
	values.Set("grant_type", RefreshTokenGrantType)
	values.Set("client_id", cfg.ClientID)
	values.Set("refresh_token", refreshToken)
	values.Set("scope", strings.Join(authScopes(cfg.Scopes), " "))
	return c.requestToken(ctx, cfg.TenantID, values)
}

func (c *AuthClient) requestToken(ctx context.Context, tenant string, values url.Values) (TokenSet, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint(tenant, "token"), strings.NewReader(values.Encode()))
	if err != nil {
		return TokenSet{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	res, err := c.httpClient().Do(req)
	if err != nil {
		return TokenSet{}, err
	}
	defer res.Body.Close()
	if res.StatusCode < http.StatusOK || res.StatusCode >= http.StatusMultipleChoices {
		return TokenSet{}, parseAuthError(res.Body, res.Status)
	}

	var parsed tokenResponse
	if err := json.NewDecoder(res.Body).Decode(&parsed); err != nil {
		return TokenSet{}, fmt.Errorf("%w: invalid token response: %v", ErrAuthFailed, err)
	}
	if strings.TrimSpace(parsed.AccessToken) == "" {
		return TokenSet{}, fmt.Errorf("%w: token response did not include an access token", ErrAuthFailed)
	}
	if parsed.TokenType == "" {
		parsed.TokenType = "Bearer"
	}
	return TokenSet{
		AccessToken:  parsed.AccessToken,
		RefreshToken: parsed.RefreshToken,
		TokenType:    parsed.TokenType,
		Scopes:       strings.Fields(parsed.Scope),
		ExpiresAt:    c.now().Add(time.Duration(parsed.ExpiresIn) * time.Second),
	}, nil
}

func (c *AuthClient) endpoint(tenant, suffix string) string {
	base := strings.TrimRight(strings.TrimSpace(c.AuthorityBaseURL), "/")
	if base == "" {
		base = DefaultAuthorityBaseURL
	}
	tenant = strings.Trim(strings.TrimSpace(tenant), "/")
	if tenant == "" {
		tenant = DefaultTenantID
	}
	return base + "/" + url.PathEscape(tenant) + "/oauth2/v2.0/" + suffix
}

func (c *AuthClient) httpClient() HTTPClient {
	if c != nil && c.HTTPClient != nil {
		return c.HTTPClient
	}
	return http.DefaultClient
}

func (c *AuthClient) now() time.Time {
	if c != nil && c.Now != nil {
		return c.Now().UTC()
	}
	return time.Now().UTC()
}

func normalizeAuthConfig(cfg GraphAuthConfig) GraphAuthConfig {
	cfg.TenantID = strings.TrimSpace(cfg.TenantID)
	if cfg.TenantID == "" {
		cfg.TenantID = DefaultTenantID
	}
	cfg.ClientID = strings.TrimSpace(cfg.ClientID)
	if cfg.AuthFlow == "" {
		cfg.AuthFlow = AuthFlowDeviceCode
	}
	if len(cfg.Scopes) == 0 {
		cfg.Scopes = append([]string(nil), DefaultGraphScopes...)
	}
	return cfg
}

func validateAuthConfig(cfg GraphAuthConfig) error {
	if strings.TrimSpace(cfg.ClientID) == "" {
		return fmt.Errorf("%w: client ID is required", ErrAuthConfigIncomplete)
	}
	if cfg.AuthFlow != "" && cfg.AuthFlow != AuthFlowDeviceCode {
		return fmt.Errorf("%w: auth flow %q is not implemented yet", ErrAuthConfigIncomplete, cfg.AuthFlow)
	}
	return nil
}

func authScopes(scopes []string) []string {
	seen := map[string]struct{}{}
	cleaned := make([]string, 0, len(scopes)+1)
	for _, scope := range scopes {
		scope = strings.TrimSpace(scope)
		if scope == "" {
			continue
		}
		if _, ok := seen[scope]; ok {
			continue
		}
		seen[scope] = struct{}{}
		cleaned = append(cleaned, scope)
	}
	if _, ok := seen[OfflineAccessScope]; !ok {
		cleaned = append(cleaned, OfflineAccessScope)
	}
	return cleaned
}

func parseAuthError(r io.Reader, status string) error {
	var parsed authErrorResponse
	_ = json.NewDecoder(r).Decode(&parsed)
	switch parsed.Error {
	case "authorization_pending":
		return ErrAuthorizationPending
	case "authorization_declined":
		return ErrAuthorizationDenied
	case "expired_token":
		return ErrDeviceCodeExpired
	case "":
		return fmt.Errorf("%w: %s", ErrAuthFailed, status)
	default:
		if parsed.ErrorDescription != "" {
			return fmt.Errorf("%w: %s: %s", ErrAuthFailed, parsed.Error, parsed.ErrorDescription)
		}
		return fmt.Errorf("%w: %s", ErrAuthFailed, parsed.Error)
	}
}
