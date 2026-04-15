package minime

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCognitoBearerTokenVerifierAcceptsValidAccessToken(t *testing.T) {
	t.Parallel()

	privateKey := mustGenerateRSAKey(t)
	kid := "cognito-test-key"
	issuer := ""
	clientID := "client-123"

	jwksServer := testCognitoJWKSServer(t, privateKey, kid)
	issuer = jwksServer.URL
	verifier, err := NewCognitoBearerTokenVerifier(issuer, clientID, "", jwksServer.Client())
	if err != nil {
		t.Fatalf("create verifier: %v", err)
	}

	token := signCognitoJWT(t, privateKey, kid, map[string]any{
		"iss":       issuer,
		"token_use": "access",
		"client_id": clientID,
		"sub":       "user-123",
		"exp":       time.Now().UTC().Add(time.Hour).Unix(),
	})

	if err := verifier.VerifyBearerToken(context.Background(), token); err != nil {
		t.Fatalf("verify token: %v", err)
	}
}

func TestCognitoBearerTokenVerifierRejectsInvalidTokens(t *testing.T) {
	t.Parallel()

	privateKey := mustGenerateRSAKey(t)
	otherPrivateKey := mustGenerateRSAKey(t)
	kid := "cognito-test-key"
	issuer := ""
	clientID := "client-123"

	jwksServer := testCognitoJWKSServer(t, privateKey, kid)
	issuer = jwksServer.URL
	verifier, err := NewCognitoBearerTokenVerifier(issuer, clientID, "", jwksServer.Client())
	if err != nil {
		t.Fatalf("create verifier: %v", err)
	}

	baseClaims := map[string]any{
		"iss":       issuer,
		"token_use": "access",
		"client_id": clientID,
		"sub":       "user-123",
		"exp":       time.Now().UTC().Add(time.Hour).Unix(),
	}

	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{
			name: "wrong issuer",
			mutate: func(claims map[string]any) {
				claims["iss"] = "https://example.invalid"
			},
		},
		{
			name: "wrong client id",
			mutate: func(claims map[string]any) {
				claims["client_id"] = "other-client"
			},
		},
		{
			name: "wrong token use",
			mutate: func(claims map[string]any) {
				claims["token_use"] = "id"
			},
		},
		{
			name: "expired token",
			mutate: func(claims map[string]any) {
				claims["exp"] = time.Now().UTC().Add(-time.Minute).Unix()
			},
		},
		{
			name: "empty subject",
			mutate: func(claims map[string]any) {
				claims["sub"] = ""
			},
		},
		{
			name:   "bad signature",
			mutate: func(claims map[string]any) {},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			claims := map[string]any{}
			for key, value := range baseClaims {
				claims[key] = value
			}
			tc.mutate(claims)

			token := signCognitoJWT(t, privateKey, kid, claims)
			if tc.name == "bad signature" {
				token = signCognitoJWT(t, otherPrivateKey, kid, claims)
			}

			if err := verifier.VerifyBearerToken(context.Background(), token); !errorsIsUnauthorized(err) {
				t.Fatalf("expected unauthorized error, got %v", err)
			}
		})
	}
}

func TestAuthMiddlewareRequiresBearerToken(t *testing.T) {
	t.Parallel()

	server := newTestServer(t)
	request := httptest.NewRequest(http.MethodPost, "/v1/minime/sessions", strings.NewReader(`{}`))
	recorder := httptest.NewRecorder()

	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("expected unauthorized status, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "missing bearer token") {
		t.Fatalf("expected missing token error, got %s", recorder.Body.String())
	}
}

func TestAuthMiddlewareRejectsInvalidBearerToken(t *testing.T) {
	t.Parallel()

	server := newTestServer(t)
	request := httptest.NewRequest(http.MethodPost, "/v1/minime/sessions", strings.NewReader(`{}`))
	request.Header.Set("Authorization", "Bearer not-the-test-token")
	recorder := httptest.NewRecorder()

	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("expected unauthorized status, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "unauthorized") {
		t.Fatalf("expected unauthorized error, got %s", recorder.Body.String())
	}
}

func TestLoadConfigParsesCognitoAuthEnv(t *testing.T) {
	t.Setenv("TONGUE_COGNITO_ISSUER", "https://cognito-idp.us-east-1.amazonaws.com/us-east-1_example")
	t.Setenv("TONGUE_COGNITO_CLIENT_ID", "client-123")
	t.Setenv("TONGUE_COGNITO_JWKS_URL", "https://example.invalid/jwks.json")

	config := LoadConfig()
	if config.CognitoIssuer != "https://cognito-idp.us-east-1.amazonaws.com/us-east-1_example" {
		t.Fatalf("expected issuer to be loaded, got %q", config.CognitoIssuer)
	}
	if config.CognitoClientID != "client-123" {
		t.Fatalf("expected client id to be loaded, got %q", config.CognitoClientID)
	}
	if config.CognitoJWKSURL != "https://example.invalid/jwks.json" {
		t.Fatalf("expected jwks url to be loaded, got %q", config.CognitoJWKSURL)
	}
}

func testCognitoJWKSServer(t *testing.T, privateKey *rsa.PrivateKey, kid string) *httptest.Server {
	t.Helper()

	jwk := rsaPublicKeyToJWK(privateKey, kid)
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/jwks.json" {
			t.Fatalf("unexpected jwks path %q", r.URL.Path)
		}
		if r.Method != http.MethodGet {
			t.Fatalf("unexpected jwks method %q", r.Method)
		}
		_ = json.NewEncoder(w).Encode(cognitoJWKS{Keys: []cognitoJWK{jwk}})
	}))
}

func rsaPublicKeyToJWK(privateKey *rsa.PrivateKey, kid string) cognitoJWK {
	return cognitoJWK{
		Kty: "RSA",
		Kid: kid,
		Use: "sig",
		Alg: "RS256",
		N:   base64.RawURLEncoding.EncodeToString(privateKey.PublicKey.N.Bytes()),
		E:   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(privateKey.PublicKey.E)).Bytes()),
	}
}

func mustGenerateRSAKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate rsa key: %v", err)
	}
	return key
}

func signCognitoJWT(t *testing.T, privateKey *rsa.PrivateKey, kid string, claims map[string]any) string {
	t.Helper()

	headerJSON, err := json.Marshal(map[string]any{
		"alg": "RS256",
		"kid": kid,
		"typ": "JWT",
	})
	if err != nil {
		t.Fatalf("marshal token header: %v", err)
	}
	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshal token claims: %v", err)
	}

	headerPart := base64.RawURLEncoding.EncodeToString(headerJSON)
	claimsPart := base64.RawURLEncoding.EncodeToString(claimsJSON)
	signingInput := headerPart + "." + claimsPart

	digest := sha256.Sum256([]byte(signingInput))
	signature, err := rsa.SignPKCS1v15(rand.Reader, privateKey, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}

	return signingInput + "." + base64.RawURLEncoding.EncodeToString(signature)
}

func errorsIsUnauthorized(err error) bool {
	return err != nil && errors.Is(err, errUnauthorizedBearerToken)
}
