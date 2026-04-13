package minime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

var errUnauthorizedBearerToken = errors.New("unauthorized bearer token")

type BearerTokenVerifier interface {
	VerifyBearerToken(ctx context.Context, token string) error
}

type SupabaseBearerTokenVerifier struct {
	userEndpointURL string
	anonKey         string
	httpClient      *http.Client
}

type supabaseUserResponse struct {
	ID string `json:"id"`
}

func NewSupabaseBearerTokenVerifier(supabaseURL, anonKey string, httpClient *http.Client) (*SupabaseBearerTokenVerifier, error) {
	normalizedURL := strings.TrimSpace(supabaseURL)
	normalizedAnonKey := strings.TrimSpace(anonKey)
	if normalizedURL == "" {
		return nil, errors.New("SUPABASE_URL is required")
	}
	if normalizedAnonKey == "" {
		return nil, errors.New("SUPABASE_ANON_KEY is required")
	}

	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	return &SupabaseBearerTokenVerifier{
		userEndpointURL: strings.TrimRight(normalizedURL, "/") + "/auth/v1/user",
		anonKey:         normalizedAnonKey,
		httpClient:      httpClient,
	}, nil
}

func (v *SupabaseBearerTokenVerifier) VerifyBearerToken(ctx context.Context, token string) error {
	normalizedToken := strings.TrimSpace(token)
	if normalizedToken == "" {
		return errUnauthorizedBearerToken
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, v.userEndpointURL, nil)
	if err != nil {
		return fmt.Errorf("build supabase auth request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+normalizedToken)
	request.Header.Set("apikey", v.anonKey)
	request.Header.Set("Accept", "application/json")

	response, err := v.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("verify supabase bearer token: %w", err)
	}
	defer response.Body.Close()

	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return errUnauthorizedBearerToken
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("supabase user lookup returned status %d", response.StatusCode)
	}

	var user supabaseUserResponse
	if err := json.NewDecoder(response.Body).Decode(&user); err != nil {
		return fmt.Errorf("decode supabase user response: %w", err)
	}
	if strings.TrimSpace(user.ID) == "" {
		return errUnauthorizedBearerToken
	}

	return nil
}

func bearerTokenVerifierForConfig(config Config) (BearerTokenVerifier, error) {
	if config.AuthVerifier != nil {
		return config.AuthVerifier, nil
	}

	return NewSupabaseBearerTokenVerifier(
		config.SupabaseURL,
		config.SupabaseAnonKey,
		config.AuthHTTPClient,
	)
}
