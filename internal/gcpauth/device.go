package gcpauth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// deviceTokenSource reuses the existing device credential nested in the
// owner-only well-known ADC carrier. It neither changes that carrier nor
// impersonates its unrelated target. The declared identity binds the source.
func (s Selector) deviceTokenSource(ctx context.Context, scopes []string, detach bool) (oauth2.TokenSource, error) {
	for _, key := range []string{"GOOGLE_APPLICATION_CREDENTIALS", "CLOUDSDK_AUTH_CREDENTIAL_FILE_OVERRIDE", "CLOUDSDK_CONFIG"} {
		if os.Getenv(key) != "" {
			return nil, fmt.Errorf("google_credentials: %s must be unset", key)
		}
	}
	path, err := defaultADCPath()
	if err != nil {
		return nil, err
	}
	raw, err := readOwnerOnlyRegularFile(path)
	if err != nil {
		return nil, err
	}
	source, err := deviceSourceConfiguration(raw, s.CredentialsIdentity)
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
	credential, err := google.CredentialsFromJSONWithType(credentialContext, source, google.ServiceAccount, scopes...)
	if err != nil {
		return nil, fmt.Errorf("google_credentials: invalid device source")
	}
	if credential == nil || credential.TokenSource == nil {
		return nil, fmt.Errorf("google_credentials: device token source unavailable")
	}
	return credential.TokenSource, nil
}

func deviceSourceConfiguration(data []byte, identity string) ([]byte, error) {
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return nil, err
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
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
