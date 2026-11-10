package gcpauth

// This profile accepts only explicitly selected, certificate-sourced Google
// workload federation. It never discovers ambient ADC, uses a shared host token, or substitutes another credential topology on failure.
// The external-account carrier impersonates only the declared service identity.
import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"cloud.google.com/go/auth"
	"cloud.google.com/go/auth/credentials"
	"golang.org/x/oauth2"
)

const workloadTokenExchangeTimeout = 30 * time.Second

var (
	workloadAudiencePattern        = regexp.MustCompile(`^//iam\.googleapis\.com/projects/[1-9][0-9]*/locations/global/workloadIdentityPools/[a-z][a-z0-9-]{2,31}/providers/[a-z][a-z0-9-]{2,31}$`)
	workloadIAMCredentialsEndpoint = "https://iamcredentials.googleapis.com"
)

func (s Selector) validateWorkloadIdentitySelector() error {
	if s.CredentialsProfile != CredentialsProfileWorkloadIdentityFederation || s.CredentialsSource != CredentialsSourceApplicationDefault || s.CredentialsFile != "" || !serviceAccountEmailPattern.MatchString(s.CredentialsIdentity) || !workloadAudiencePattern.MatchString(s.CredentialsAudience) {
		return fmt.Errorf("google_credentials: workload federation requires an exact audience and scoped identity")
	}
	if base := strings.TrimSpace(s.CredentialsBaseIdentity); base != "" {
		if !serviceAccountEmailPattern.MatchString(base) || strings.EqualFold(base, strings.TrimSpace(s.CredentialsIdentity)) {
			return fmt.Errorf("google_credentials: workload base identity must be a distinct canonical service-account email")
		}
	}
	return nil
}

type workloadCarrier struct {
	Type             string `json:"type"`
	Audience         string `json:"audience"`
	SubjectTokenType string `json:"subject_token_type"`
	TokenURL         string `json:"token_url"`
	ImpersonationURL string `json:"service_account_impersonation_url"`
	UniverseDomain   string `json:"universe_domain,omitempty"`
	QuotaProject     string `json:"quota_project_id,omitempty"`
	CredentialSource struct {
		Certificate struct {
			Config         string `json:"certificate_config_location"`
			TrustChainPath string `json:"trust_chain_path,omitempty"`
		} `json:"certificate"`
	} `json:"credential_source"`
}
type workloadCertificateConfiguration struct {
	Version        int    `json:"version,omitempty"`
	TrustChainPath string `json:"trust_chain_path,omitempty"`
	CertConfigs    struct {
		Workload struct {
			Cert           string `json:"cert_path"`
			Key            string `json:"key_path"`
			TrustChainPath string `json:"trust_chain_path,omitempty"`
		} `json:"workload"`
	} `json:"cert_configs"`
}

func decodeWorkloadJSON(data []byte, into any) error {
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return fmt.Errorf("google_credentials: invalid workload JSON")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(into); err != nil {
		return fmt.Errorf("google_credentials: unsupported workload configuration")
	}
	return nil
}

// ValidateWorkloadIdentityConfiguration validates the entire declared local
// credential boundary without exchanging a token. The native MCP bridge calls
// this same implementation instead of maintaining a second shell validator.
func (s Selector) ValidateWorkloadIdentityConfiguration() ([]byte, error) {
	if err := validateWorkloadTransportEnvironment(); err != nil {
		return nil, err
	}
	if err := s.validateWorkloadIdentitySelector(); err != nil {
		return nil, err
	}
	for _, key := range []string{"CLOUDSDK_AUTH_CREDENTIAL_FILE_OVERRIDE", "CLOUDSDK_AUTH_ACCESS_TOKEN", "CLOUDSDK_AUTH_ACCESS_TOKEN_FILE", "CLOUDSDK_AUTH_IMPERSONATE_SERVICE_ACCOUNT", "CLOUDSDK_CONFIG", "GOOGLE_GHA_CREDS_PATH", "GOOGLE_API_CERTIFICATE_CONFIG", "GCE_METADATA_HOST", "GOOGLE_EXTERNAL_ACCOUNT_ALLOW_EXECUTABLES"} {
		if os.Getenv(key) != "" {
			return nil, fmt.Errorf("google_credentials: ambient workload credential override is forbidden")
		}
	}
	path := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS")
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, fmt.Errorf("google_credentials: workload carrier must be explicitly selected by absolute path")
	}
	raw, err := readOwnerOnlyRegularFile(path)
	if err != nil {
		return nil, fmt.Errorf("google_credentials: workload carrier is not owner-protected")
	}
	var carrier workloadCarrier
	if err := decodeWorkloadJSON(raw, &carrier); err != nil {
		return nil, err
	}
	carrierIdentity := strings.TrimSpace(s.CredentialsBaseIdentity)
	if carrierIdentity == "" {
		carrierIdentity = strings.TrimSpace(s.CredentialsIdentity)
	}
	expectedURL := "https://iamcredentials.mtls.googleapis.com/v1/projects/-/serviceAccounts/" + carrierIdentity + ":generateAccessToken"
	if carrier.Type != "external_account" || carrier.Audience != s.CredentialsAudience || carrier.SubjectTokenType != "urn:ietf:params:oauth:token-type:mtls" || carrier.TokenURL != "https://sts.mtls.googleapis.com/v1/token" || carrier.ImpersonationURL != expectedURL || (carrier.UniverseDomain != "" && carrier.UniverseDomain != "googleapis.com") {
		return nil, fmt.Errorf("google_credentials: workload service identity, audience or endpoint does not match")
	}
	if carrier.QuotaProject != "" && carrier.QuotaProject != serviceAccountProject(carrierIdentity) {
		return nil, fmt.Errorf("google_credentials: workload quota project does not match")
	}
	certConfig := carrier.CredentialSource.Certificate.Config
	if !filepath.IsAbs(certConfig) || filepath.Clean(certConfig) != certConfig {
		return nil, fmt.Errorf("google_credentials: explicit workload certificate configuration is required")
	}
	data, err := readOwnerOnlyRegularFile(certConfig)
	if err != nil {
		return nil, fmt.Errorf("google_credentials: workload certificate configuration is not owner-protected")
	}
	var certificate workloadCertificateConfiguration
	if err := decodeWorkloadJSON(data, &certificate); err != nil {
		return nil, err
	}
	var material [2][]byte
	for i, file := range []string{certificate.CertConfigs.Workload.Cert, certificate.CertConfigs.Workload.Key} {
		if !filepath.IsAbs(file) || filepath.Clean(file) != file {
			return nil, fmt.Errorf("google_credentials: explicit workload certificate material is required")
		}
		material[i], err = readOwnerOnlyRegularFile(file)
		if err != nil {
			return nil, fmt.Errorf("google_credentials: workload certificate material is not owner-protected")
		}
	}
	if _, err := tls.X509KeyPair(material[0], material[1]); err != nil {
		return nil, fmt.Errorf("google_credentials: workload certificate and key are invalid")
	}
	return raw, nil
}

func (s Selector) workloadIdentityTokenSource(ctx context.Context, scopes []string, detach bool) (oauth2.TokenSource, error) {
	raw, err := s.ValidateWorkloadIdentityConfiguration()
	if err != nil {
		return nil, err
	}
	if len(scopes) != 1 || scopes[0] != CloudPlatformScope {
		return nil, fmt.Errorf("google_credentials: workload scope must be cloud-platform")
	}
	source, err := credentials.NewCredentialsFromJSON(credentials.ExternalAccount, raw, &credentials.DetectOptions{Scopes: scopes})
	if err != nil {
		return nil, fmt.Errorf("google_credentials: workload credential construction failed")
	}
	if detach {
		ctx = context.WithoutCancel(ctx)
	}
	hop1 := &workloadOAuthTokenSource{ctx: ctx, provider: source}
	if strings.TrimSpace(s.CredentialsBaseIdentity) == "" {
		return hop1, nil
	}
	targetIdentity := strings.TrimSpace(s.CredentialsIdentity)
	hop2 := &workloadImpersonatedTokenSource{
		ctx:            ctx,
		base:           hop1,
		targetIdentity: targetIdentity,
		quotaProject:   serviceAccountProject(targetIdentity),
		scopes:         append([]string(nil), scopes...),
		client:         &http.Client{Timeout: workloadTokenExchangeTimeout},
	}
	return oauth2.ReuseTokenSource(nil, hop2), nil
}

type workloadOAuthTokenSource struct {
	ctx      context.Context
	provider auth.TokenProvider
}

func (s *workloadOAuthTokenSource) Token() (*oauth2.Token, error) {
	callCtx, cancel := context.WithTimeout(s.ctx, workloadTokenExchangeTimeout)
	defer cancel()
	t, err := s.provider.Token(callCtx)
	if err != nil {
		return nil, fmt.Errorf("google_credentials: workload token exchange failed")
	}
	if t == nil || strings.TrimSpace(t.Value) == "" {
		return nil, fmt.Errorf("google_credentials: workload exchange returned no token")
	}
	return &oauth2.Token{AccessToken: t.Value, TokenType: t.Type, Expiry: t.Expiry}, nil
}

type workloadImpersonatedTokenSource struct {
	ctx            context.Context
	base           oauth2.TokenSource
	targetIdentity string
	quotaProject   string
	scopes         []string
	client         *http.Client
}

func (s *workloadImpersonatedTokenSource) Token() (*oauth2.Token, error) {
	baseToken, err := s.base.Token()
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(struct {
		Scope []string `json:"scope"`
	}{Scope: s.scopes})
	if err != nil {
		return nil, fmt.Errorf("google_credentials: encode target impersonation request")
	}
	callCtx, cancel := context.WithTimeout(s.ctx, workloadTokenExchangeTimeout)
	defer cancel()
	endpoint := strings.TrimRight(workloadIAMCredentialsEndpoint, "/") + "/v1/projects/-/serviceAccounts/" + url.PathEscape(s.targetIdentity) + ":generateAccessToken"
	req, err := http.NewRequestWithContext(callCtx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("google_credentials: build target impersonation request")
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(baseToken.AccessToken))
	req.Header.Set("Content-Type", "application/json")
	if s.quotaProject != "" {
		req.Header.Set("x-goog-user-project", s.quotaProject)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("google_credentials: workload target impersonation failed")
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxCredentialConfigurationBytes))
	if err != nil || resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("google_credentials: workload target impersonation rejected")
	}
	var tokenResp struct {
		AccessToken string `json:"accessToken"`
		ExpireTime  string `json:"expireTime"`
	}
	if err := decodeWorkloadJSON(raw, &tokenResp); err != nil {
		return nil, fmt.Errorf("google_credentials: invalid workload target token response")
	}
	expiry, err := time.Parse(time.RFC3339Nano, tokenResp.ExpireTime)
	if err != nil || strings.TrimSpace(tokenResp.AccessToken) == "" {
		return nil, fmt.Errorf("google_credentials: incomplete workload target token response")
	}
	return &oauth2.Token{AccessToken: tokenResp.AccessToken, TokenType: "Bearer", Expiry: expiry}, nil
}

// The explicit certificate configuration is the sole mTLS credential input.
// Reject transport routing/logging overrides before loading any private material.
func validateWorkloadTransportEnvironment() error {
	for _, key := range []string{
		"EXPERIMENTAL_GOOGLE_API_USE_S2A", "GRPC_BINARY_LOG_FILTER", "GRPC_ENFORCE_ALPN_ENABLED",
		"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy",
		"GOOGLE_API_GO_EXPERIMENTAL_DISABLE_NEW_AUTH_LIB", "GOOGLE_API_GO_EXPERIMENTAL_ENABLE_NEW_AUTH_LIB",
		"GOOGLE_API_USE_MTLS", "GOOGLE_AUTH_TRUST_BOUNDARY_ENABLED", "GOOGLE_CLOUD_DISABLE_DIRECT_PATH",
		"GOOGLE_CLOUD_ENABLE_DIRECT_PATH_XDS", "GOOGLE_CLOUD_QUOTA_PROJECT", "GOOGLE_CLOUD_UNIVERSE_DOMAIN",
		"GOOGLE_SPANNER_DISABLE_DIRECT_ACCESS_BOUND_TOKEN", "GOOGLE_SPANNER_ENABLE_DIRECT_ACCESS",
		"GOOGLE_SPANNER_ENABLE_GCP_FALLBACK", "GOOGLE_SPANNER_EXPERIMENTAL_LOCATION_API", "SPANNER_MONITORING_HOST",
		"SSL_CERT_DIR", "SSL_CERT_FILE", "GOOGLE_SDK_GO_LOGGING_LEVEL",
	} {
		if os.Getenv(key) != "" {
			return fmt.Errorf("google_credentials: ambient workload transport override is forbidden")
		}
	}
	for key, expected := range map[string]string{"GOOGLE_API_USE_CLIENT_CERTIFICATE": "false", "GOOGLE_API_USE_MTLS_ENDPOINT": "never"} {
		if value := os.Getenv(key); value != "" && value != expected {
			return fmt.Errorf("google_credentials: ambient workload transport override is forbidden")
		}
	}
	for _, fragment := range []string{"http2debug=", "tlsmlkem=0", "tlssecpmlkem=0"} {
		if strings.Contains(os.Getenv("GODEBUG"), fragment) {
			return fmt.Errorf("google_credentials: workload transport debugging or post-quantum downgrade is forbidden")
		}
	}
	return nil
}
