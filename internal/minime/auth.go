package minime

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"
)

var errUnauthorizedBearerToken = errors.New("unauthorized bearer token")

type BearerTokenVerifier interface {
	VerifyBearerToken(ctx context.Context, token string) error
}

type StaticBearerTokenVerifier struct {
	token string
}

type AnyBearerTokenVerifier struct {
	verifiers []BearerTokenVerifier
}

type CognitoBearerTokenVerifier struct {
	issuer     string
	clientID   string
	jwksURL    string
	httpClient *http.Client

	mu       sync.Mutex
	keyCache map[string]*rsa.PublicKey
}

type cognitoJWKS struct {
	Keys []cognitoJWK `json:"keys"`
}

type cognitoJWK struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Use string `json:"use"`
	Alg string `json:"alg"`
	N   string `json:"n"`
	E   string `json:"e"`
}

type cognitoJWTHeader struct {
	Alg string `json:"alg"`
	Kid string `json:"kid"`
}

type cognitoJWTClaims struct {
	Iss      string      `json:"iss"`
	TokenUse string      `json:"token_use"`
	ClientID string      `json:"client_id"`
	Sub      string      `json:"sub"`
	Exp      json.Number `json:"exp"`
}

func NewCognitoBearerTokenVerifier(issuer, clientID, jwksURL string, httpClient *http.Client) (*CognitoBearerTokenVerifier, error) {
	normalizedIssuer := strings.TrimSpace(strings.TrimRight(issuer, "/"))
	normalizedClientID := strings.TrimSpace(clientID)
	normalizedJWKSURL := strings.TrimSpace(jwksURL)

	if normalizedIssuer == "" {
		return nil, errors.New("TONGUE_COGNITO_ISSUER is required")
	}
	if normalizedClientID == "" {
		return nil, errors.New("TONGUE_COGNITO_CLIENT_ID is required")
	}
	if normalizedJWKSURL == "" {
		normalizedJWKSURL = normalizedIssuer + "/.well-known/jwks.json"
	}
	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	return &CognitoBearerTokenVerifier{
		issuer:     normalizedIssuer,
		clientID:   normalizedClientID,
		jwksURL:    normalizedJWKSURL,
		httpClient: httpClient,
		keyCache:   map[string]*rsa.PublicKey{},
	}, nil
}

func NewStaticBearerTokenVerifier(token string) (*StaticBearerTokenVerifier, error) {
	normalizedToken := strings.TrimSpace(token)
	if normalizedToken == "" {
		return nil, errors.New("MINIME_INTERNAL_BEARER_TOKEN is required")
	}
	return &StaticBearerTokenVerifier{token: normalizedToken}, nil
}

func (v *StaticBearerTokenVerifier) VerifyBearerToken(_ context.Context, token string) error {
	if v == nil || strings.TrimSpace(v.token) == "" {
		return errors.New("static bearer token verifier is not configured")
	}
	if strings.TrimSpace(token) != v.token {
		return errUnauthorizedBearerToken
	}
	return nil
}

func NewAnyBearerTokenVerifier(verifiers ...BearerTokenVerifier) (*AnyBearerTokenVerifier, error) {
	filtered := make([]BearerTokenVerifier, 0, len(verifiers))
	for _, verifier := range verifiers {
		if verifier != nil {
			filtered = append(filtered, verifier)
		}
	}
	if len(filtered) == 0 {
		return nil, errors.New("at least one bearer token verifier is required")
	}
	return &AnyBearerTokenVerifier{verifiers: filtered}, nil
}

func (v *AnyBearerTokenVerifier) VerifyBearerToken(ctx context.Context, token string) error {
	if v == nil || len(v.verifiers) == 0 {
		return errors.New("compound bearer token verifier is not configured")
	}

	var firstNonUnauthorized error
	for _, verifier := range v.verifiers {
		err := verifier.VerifyBearerToken(ctx, token)
		if err == nil {
			return nil
		}
		if errors.Is(err, errUnauthorizedBearerToken) {
			continue
		}
		if firstNonUnauthorized == nil {
			firstNonUnauthorized = err
		}
	}
	if firstNonUnauthorized != nil {
		return firstNonUnauthorized
	}
	return errUnauthorizedBearerToken
}

func (v *CognitoBearerTokenVerifier) VerifyBearerToken(ctx context.Context, token string) error {
	normalizedToken := strings.TrimSpace(token)
	if normalizedToken == "" {
		return errUnauthorizedBearerToken
	}

	parts := strings.Split(normalizedToken, ".")
	if len(parts) != 3 {
		return errUnauthorizedBearerToken
	}

	var header cognitoJWTHeader
	if err := decodeJWTPart(parts[0], &header); err != nil {
		return errUnauthorizedBearerToken
	}
	if strings.TrimSpace(header.Alg) != "RS256" || strings.TrimSpace(header.Kid) == "" {
		return errUnauthorizedBearerToken
	}

	publicKey, err := v.publicKeyForKid(ctx, header.Kid)
	if err != nil {
		return err
	}

	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return errUnauthorizedBearerToken
	}

	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(publicKey, crypto.SHA256, digest[:], signature); err != nil {
		return errUnauthorizedBearerToken
	}

	var claims cognitoJWTClaims
	if err := decodeJWTPart(parts[1], &claims); err != nil {
		return errUnauthorizedBearerToken
	}

	if strings.TrimSpace(claims.Iss) != v.issuer {
		return errUnauthorizedBearerToken
	}
	if strings.TrimSpace(claims.TokenUse) != "access" {
		return errUnauthorizedBearerToken
	}
	if strings.TrimSpace(claims.ClientID) != v.clientID {
		return errUnauthorizedBearerToken
	}
	if strings.TrimSpace(claims.Sub) == "" {
		return errUnauthorizedBearerToken
	}

	exp, err := claims.Exp.Int64()
	if err != nil || exp <= 0 {
		return errUnauthorizedBearerToken
	}
	if time.Now().UTC().Unix() >= exp {
		return errUnauthorizedBearerToken
	}

	return nil
}

func (v *CognitoBearerTokenVerifier) publicKeyForKid(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	if v == nil {
		return nil, errors.New("cognito bearer token verifier is not configured")
	}

	if key := v.cachedPublicKey(kid); key != nil {
		return key, nil
	}

	keys, err := v.fetchJWKS(ctx)
	if err != nil {
		return nil, err
	}

	v.mu.Lock()
	v.keyCache = keys
	key := v.keyCache[kid]
	v.mu.Unlock()
	if key == nil {
		return nil, errUnauthorizedBearerToken
	}

	return key, nil
}

func (v *CognitoBearerTokenVerifier) cachedPublicKey(kid string) *rsa.PublicKey {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.keyCache[kid]
}

func (v *CognitoBearerTokenVerifier) fetchJWKS(ctx context.Context) (map[string]*rsa.PublicKey, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, v.jwksURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build cognito jwks request: %w", err)
	}
	request.Header.Set("Accept", "application/json")

	response, err := v.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("fetch cognito jwks: %w", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("cognito jwks returned status %d", response.StatusCode)
	}

	var set cognitoJWKS
	if err := json.NewDecoder(response.Body).Decode(&set); err != nil {
		return nil, fmt.Errorf("decode cognito jwks response: %w", err)
	}

	keys := make(map[string]*rsa.PublicKey, len(set.Keys))
	for _, jwk := range set.Keys {
		publicKey, err := jwk.publicKey()
		if err != nil {
			continue
		}
		if publicKey == nil || strings.TrimSpace(jwk.Kid) == "" {
			continue
		}
		keys[strings.TrimSpace(jwk.Kid)] = publicKey
	}
	if len(keys) == 0 {
		return nil, errors.New("cognito jwks did not contain any usable RSA keys")
	}

	return keys, nil
}

func (jwk cognitoJWK) publicKey() (*rsa.PublicKey, error) {
	if strings.TrimSpace(jwk.Kty) != "RSA" {
		return nil, errors.New("unsupported jwk key type")
	}

	modulus, err := base64.RawURLEncoding.DecodeString(jwk.N)
	if err != nil {
		return nil, fmt.Errorf("decode jwk modulus: %w", err)
	}
	exponentBytes, err := base64.RawURLEncoding.DecodeString(jwk.E)
	if err != nil {
		return nil, fmt.Errorf("decode jwk exponent: %w", err)
	}

	exponent := new(big.Int).SetBytes(exponentBytes).Int64()
	if exponent <= 0 || exponent > int64(^uint(0)>>1) {
		return nil, errors.New("invalid jwk exponent")
	}

	return &rsa.PublicKey{
		N: new(big.Int).SetBytes(modulus),
		E: int(exponent),
	}, nil
}

func decodeJWTPart(rawPart string, target any) error {
	decoded, err := base64.RawURLEncoding.DecodeString(rawPart)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(decoded, target); err != nil {
		return err
	}
	return nil
}

func bearerTokenVerifierForConfig(config Config) (BearerTokenVerifier, error) {
	if config.AuthVerifier != nil {
		return config.AuthVerifier, nil
	}

	mode := strings.TrimSpace(strings.ToLower(config.AuthMode))
	if mode == "" {
		mode = "cognito"
	}

	switch mode {
	case "cognito":
		return NewCognitoBearerTokenVerifier(
			config.CognitoIssuer,
			config.CognitoClientID,
			config.CognitoJWKSURL,
			config.AuthHTTPClient,
		)
	case "internal":
		return NewStaticBearerTokenVerifier(config.InternalBearerToken)
	case "cognito_or_internal":
		cognitoVerifier, err := NewCognitoBearerTokenVerifier(
			config.CognitoIssuer,
			config.CognitoClientID,
			config.CognitoJWKSURL,
			config.AuthHTTPClient,
		)
		if err != nil {
			return nil, err
		}
		internalVerifier, err := NewStaticBearerTokenVerifier(config.InternalBearerToken)
		if err != nil {
			return nil, err
		}
		return NewAnyBearerTokenVerifier(cognitoVerifier, internalVerifier)
	default:
		return nil, fmt.Errorf("unsupported auth mode %q", config.AuthMode)
	}
}
