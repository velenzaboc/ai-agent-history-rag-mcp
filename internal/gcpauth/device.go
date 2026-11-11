package gcpauth

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"cloud.google.com/go/auth/credentials"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// deviceTokenSource resolves the workstation's kopia-<host> credential from
// ~/.config/no13-backup/kopia-<host>.json (either a service_account key or an
// external_account X.509 certificate config) or falls back to the nested
// source_credentials in the well-known ADC carrier.
func (s Selector) deviceTokenSource(ctx context.Context, scopes []string, detach bool) (oauth2.TokenSource, error) {
	for _, key := range []string{"GOOGLE_APPLICATION_CREDENTIALS", "CLOUDSDK_AUTH_CREDENTIAL_FILE_OVERRIDE", "CLOUDSDK_CONFIG"} {
		if os.Getenv(key) != "" {
			return nil, fmt.Errorf("google_credentials: %s must be unset", key)
		}
	}
	path, err := s.resolveDeviceCarrierPath()
	if err != nil {
		return nil, err
	}
	raw, err := readOwnerOnlyRegularFile(path)
	if err != nil {
		return nil, err
	}
	credentialContext := ctx
	if detach {
		credentialContext = context.Background()
		if client, ok := ctx.Value(oauth2.HTTPClient).(*http.Client); ok && client != nil {
			credentialContext = context.WithValue(credentialContext, oauth2.HTTPClient, client)
		}
	}
	if isExternalAccountCarrier(raw) {
		if err := validateDeviceWorkloadConfiguration(raw, s.CredentialsIdentity); err != nil {
			return nil, err
		}
		source, err := credentials.NewCredentialsFromJSON(credentials.ExternalAccount, raw, &credentials.DetectOptions{Scopes: scopes})
		if err != nil {
			return nil, fmt.Errorf("google_credentials: invalid device workload source")
		}
		return &workloadOAuthTokenSource{ctx: credentialContext, provider: source}, nil
	}
	source, err := deviceSourceConfiguration(raw, s.CredentialsIdentity)
	if err != nil {
		return nil, err
	}
	credential, err := google.CredentialsFromJSONWithType(credentialContext, source, google.ServiceAccount, scopes...)
	if err != nil {
		return nil, fmt.Errorf("google_credentials: invalid device source")
	}
	if credential == nil || credential.TokenSource == nil {
		return nil, fmt.Errorf("google_credentials: device token source unavailable")
	}
	return credential.TokenSource, nil
}

func (s Selector) resolveDeviceCarrierPath() (string, error) {
	if localPart, _, ok := strings.Cut(strings.TrimSpace(s.CredentialsIdentity), "@"); ok && strings.HasPrefix(localPart, "kopia-") {
		if home := strings.TrimSpace(os.Getenv("HOME")); home != "" && filepath.IsAbs(home) {
			candidate := filepath.Join(home, ".config", "no13-backup", localPart+".json")
			if _, err := os.Lstat(candidate); err == nil {
				return candidate, nil
			}
		}
	}
	return defaultADCPath()
}

func isExternalAccountCarrier(data []byte) bool {
	var header struct {
		Type string `json:"type"`
	}
	return json.Unmarshal(data, &header) == nil && header.Type == "external_account"
}

func validateDeviceWorkloadConfiguration(raw []byte, identity string) error {
	if err := validateWorkloadTransportEnvironment(); err != nil {
		return err
	}
	var carrier workloadCarrier
	if err := decodeWorkloadJSON(raw, &carrier); err != nil {
		return err
	}
	expectedURL := "https://iamcredentials.mtls.googleapis.com/v1/projects/-/serviceAccounts/" + strings.TrimSpace(identity) + ":generateAccessToken"
	if carrier.Type != "external_account" || !workloadAudiencePattern.MatchString(carrier.Audience) || carrier.SubjectTokenType != "urn:ietf:params:oauth:token-type:mtls" || carrier.TokenURL != "https://sts.mtls.googleapis.com/v1/token" || carrier.ImpersonationURL != expectedURL || (carrier.UniverseDomain != "" && carrier.UniverseDomain != "googleapis.com") {
		return fmt.Errorf("google_credentials: device workload identity, audience or endpoint does not match")
	}
	if carrier.QuotaProject != "" && carrier.QuotaProject != serviceAccountProject(identity) {
		return fmt.Errorf("google_credentials: device workload quota project does not match")
	}
	certConfig := carrier.CredentialSource.Certificate.Config
	if !filepath.IsAbs(certConfig) || filepath.Clean(certConfig) != certConfig {
		return fmt.Errorf("google_credentials: explicit device certificate configuration is required")
	}
	data, err := readOwnerOnlyRegularFile(certConfig)
	if err != nil {
		return fmt.Errorf("google_credentials: device certificate configuration is not owner-protected")
	}
	var certificate workloadCertificateConfiguration
	if err := decodeWorkloadJSON(data, &certificate); err != nil {
		return err
	}
	var material [2][]byte
	for i, file := range []string{certificate.CertConfigs.Workload.Cert, certificate.CertConfigs.Workload.Key} {
		if !filepath.IsAbs(file) || filepath.Clean(file) != file {
			return fmt.Errorf("google_credentials: explicit device certificate material is required")
		}
		material[i], err = readOwnerOnlyRegularFile(file)
		if err != nil {
			return fmt.Errorf("google_credentials: device certificate material is not owner-protected")
		}
	}
	if _, err := tls.X509KeyPair(material[0], material[1]); err != nil {
		return fmt.Errorf("google_credentials: device certificate and key are invalid")
	}
	return nil
}

func deviceSourceConfiguration(data []byte, identity string) ([]byte, error) {
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return nil, err
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	if raw["type"] == "service_account" {
		if raw["client_email"] != identity {
			return nil, fmt.Errorf("google_credentials: device source must match credentials_identity")
		}
		synthetic := map[string]any{
			"type":                              CredentialsProfileImpersonatedServiceAccount,
			"service_account_impersonation_url": "https://iamcredentials.googleapis.com/v1/projects/-/serviceAccounts/" + identity + ":generateAccessToken",
			"quota_project_id":                  serviceAccountProject(identity),
			"scopes":                            []string{CloudPlatformScope},
			"delegates":                         []string{},
			"source_credentials":                raw,
		}
		validated, err := json.Marshal(synthetic)
		if err != nil {
			return nil, err
		}
		if err := validateImpersonatedConfiguration(validated, identity); err != nil {
			return nil, err
		}
		return json.Marshal(raw)
	}
	source, ok := raw["source_credentials"].(map[string]any)
	if !ok || source["type"] != "service_account" || source["client_email"] != identity {
		return nil, fmt.Errorf("google_credentials: device source must match credentials_identity")
	}
	// Reuse the closed carrier/source validator. Missing impersonation-only
	// metadata is supplied solely in this private in-memory validation copy.
	targetURL, _ := raw["service_account_impersonation_url"].(string)
	parsed, err := url.Parse(targetURL)
	if err != nil {
		return nil, fmt.Errorf("invalid carrier target")
	}
	target := strings.TrimSuffix(strings.TrimPrefix(parsed.Path, "/v1/projects/-/serviceAccounts/"), ":generateAccessToken")
	if !serviceAccountEmailPattern.MatchString(target) {
		return nil, fmt.Errorf("invalid carrier target")
	}
	if _, ok := raw["quota_project_id"]; !ok {
		raw["quota_project_id"] = serviceAccountProject(target)
	}
	if _, ok := raw["scopes"]; !ok {
		raw["scopes"] = []string{CloudPlatformScope}
	}
	if _, ok := raw["delegates"]; !ok {
		raw["delegates"] = []string{}
	}
	validated, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	if err = validateImpersonatedConfiguration(validated, target); err != nil {
		return nil, err
	}
	return json.Marshal(source)
}
