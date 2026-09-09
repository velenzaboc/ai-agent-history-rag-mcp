package gcpauth

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/oauth2"
)

type failingMetadataClient struct {
	onGCE    bool
	email    string
	emailErr error
	tokenErr error
}

func (f *failingMetadataClient) OnGCEWithContext(context.Context) bool { return f.onGCE }
func (f *failingMetadataClient) EmailWithContext(context.Context, string) (string, error) {
	return f.email, f.emailErr
}
func (f *failingMetadataClient) GetWithContext(context.Context, string) (string, error) {
	return "", f.tokenErr
}

func TestSelectorCoversDefaultPathAndEarlyValidationBoundaries(t *testing.T) {
	clearAmbientGoogleEnvironment(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	path, err := defaultADCPath()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".config", "gcloud", "application_default_credentials.json")
	if path != want {
		t.Fatalf("default ADC path = %q, want %q", path, want)
	}

	if err := (Selector{}).Validate(""); err == nil || !strings.Contains(err.Error(), "google_credentials") {
		t.Fatalf("empty-label validation error = %v", err)
	}
	selector := Selector{
		CredentialsSource:   CredentialsSourceApplicationDefault,
		CredentialsProfile:  CredentialsProfileAttachedServiceAccount,
		CredentialsIdentity: testIdentity,
	}
	if _, err := selector.TokenSource(nil); err == nil || !strings.Contains(err.Error(), "context is required") {
		t.Fatalf("nil-context error = %v", err)
	}

	originalMetadata := newMetadataClient
	newMetadataClient = func() metadataClient { return nil }
	t.Cleanup(func() { newMetadataClient = originalMetadata })
	if _, err := selector.TokenSourceForRequest(context.Background()); err == nil || !strings.Contains(err.Error(), "requires Google-hosted metadata") {
		t.Fatalf("default-scope metadata error = %v", err)
	}
}

func TestSealAmbientGoogleTransportAcceptsOnlySealedValues(t *testing.T) {
	clearAmbientGoogleEnvironment(t)
	t.Setenv("GOOGLE_API_USE_CLIENT_CERTIFICATE", sealedGoogleClientCertificate)
	t.Setenv("GOOGLE_API_USE_MTLS_ENDPOINT", sealedGoogleMTLSEndpoint)
	if err := sealAmbientGoogleTransport(); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("GOOGLE_API_USE_CLIENT_CERTIFICATE"); got != sealedGoogleClientCertificate {
		t.Fatalf("client-certificate seal = %q", got)
	}
	if got := os.Getenv("GOOGLE_API_USE_MTLS_ENDPOINT"); got != sealedGoogleMTLSEndpoint {
		t.Fatalf("mTLS endpoint seal = %q", got)
	}

	for _, tt := range []struct {
		key   string
		value string
	}{
		{key: "GOOGLE_API_USE_CLIENT_CERTIFICATE", value: "true"},
		{key: "GOOGLE_API_USE_MTLS_ENDPOINT", value: "auto"},
	} {
		t.Run(tt.key, func(t *testing.T) {
			clearAmbientGoogleEnvironment(t)
			t.Setenv(tt.key, tt.value)
			if err := sealAmbientGoogleTransport(); err == nil || !strings.Contains(err.Error(), tt.key) {
				t.Fatalf("seal error = %v", err)
			}
		})
	}
}

func TestImpersonatedTokenSourceFailureAndDetachedContextBoundaries(t *testing.T) {
	clearAmbientGoogleEnvironment(t)
	selector := Selector{
		CredentialsSource:   CredentialsSourceApplicationDefault,
		CredentialsProfile:  CredentialsProfileImpersonatedServiceAccount,
		CredentialsIdentity: testIdentity,
	}
	originalPath := defaultADCPath
	t.Cleanup(func() { defaultADCPath = originalPath })

	for _, key := range []string{"CLOUDSDK_AUTH_CREDENTIAL_FILE_OVERRIDE", "CLOUDSDK_CONFIG"} {
		t.Run(key, func(t *testing.T) {
			clearAmbientGoogleEnvironment(t)
			t.Setenv(key, "/forbidden")
			if _, err := selector.TokenSourceForRequest(context.Background(), CloudPlatformScope); err == nil || !strings.Contains(err.Error(), key) {
				t.Fatalf("override error = %v", err)
			}
		})
	}

	clearAmbientGoogleEnvironment(t)
	defaultADCPath = func() (string, error) { return "", errors.New("path unavailable") }
	if _, err := selector.TokenSourceForRequest(context.Background(), CloudPlatformScope); err == nil || !strings.Contains(err.Error(), "path unavailable") {
		t.Fatalf("path resolution error = %v", err)
	}

	defaultADCPath = func() (string, error) { return filepath.Join(t.TempDir(), "missing.json"), nil }
	if _, err := selector.TokenSourceForRequest(context.Background(), CloudPlatformScope); err == nil || !strings.Contains(err.Error(), "read impersonated ADC carrier") {
		t.Fatalf("missing carrier error = %v", err)
	}

	path := filepath.Join(t.TempDir(), "application_default_credentials.json")
	if err := os.WriteFile(path, []byte(`{"type":`), 0o600); err != nil {
		t.Fatal(err)
	}
	defaultADCPath = func() (string, error) { return path, nil }
	if _, err := selector.TokenSourceForRequest(context.Background(), CloudPlatformScope); err == nil || !strings.Contains(err.Error(), "validate impersonated ADC carrier") {
		t.Fatalf("invalid carrier error = %v", err)
	}

	if err := os.WriteFile(path, []byte(validImpersonatedCarrier(testIdentity)), 0o600); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{}
	ctx := context.WithValue(context.Background(), oauth2.HTTPClient, client)
	if source, err := selector.TokenSource(ctx, CloudPlatformScope); err != nil || source == nil {
		t.Fatalf("detached token source = %v, error = %v", source, err)
	}
}

func TestAttachedTokenSourceMetadataAndLifetimeBoundaries(t *testing.T) {
	clearAmbientGoogleEnvironment(t)
	selector := Selector{
		CredentialsSource:   CredentialsSourceApplicationDefault,
		CredentialsProfile:  CredentialsProfileAttachedServiceAccount,
		CredentialsIdentity: testIdentity,
	}
	originalMetadata := newMetadataClient
	originalCompute := computeTokenSource
	t.Cleanup(func() {
		newMetadataClient = originalMetadata
		computeTokenSource = originalCompute
	})

	t.Setenv("CLOUDSDK_AUTH_CREDENTIAL_FILE_OVERRIDE", "/forbidden")
	if _, err := selector.TokenSourceForRequest(context.Background(), CloudPlatformScope); err == nil || !strings.Contains(err.Error(), "CLOUDSDK_AUTH_CREDENTIAL_FILE_OVERRIDE") {
		t.Fatalf("attached override error = %v", err)
	}
	clearAmbientGoogleEnvironment(t)

	newMetadataClient = func() metadataClient {
		return &failingMetadataClient{onGCE: true, emailErr: errors.New("metadata email unavailable")}
	}
	if _, err := selector.TokenSourceForRequest(context.Background(), CloudPlatformScope); err == nil || !strings.Contains(err.Error(), "metadata email unavailable") {
		t.Fatalf("metadata email error = %v", err)
	}

	newMetadataClient = func() metadataClient {
		return &failingMetadataClient{onGCE: true, email: testIdentity}
	}
	computeTokenSource = func(string, ...string) oauth2.TokenSource { return nil }
	if _, err := selector.TokenSource(context.Background(), CloudPlatformScope); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("nil compute token source error = %v", err)
	}

	wantToken := &oauth2.Token{AccessToken: "attached-token"}
	computeTokenSource = func(account string, scopes ...string) oauth2.TokenSource {
		if account != testIdentity || len(scopes) != 1 || scopes[0] != CloudPlatformScope {
			t.Fatalf("compute token source arguments = %q, %v", account, scopes)
		}
		return oauth2.StaticTokenSource(wantToken)
	}
	source, err := selector.TokenSource(context.Background(), CloudPlatformScope)
	if err != nil {
		t.Fatal(err)
	}
	got, err := source.Token()
	if err != nil || got.AccessToken != wantToken.AccessToken {
		t.Fatalf("attached lifetime token = %#v, error = %v", got, err)
	}

	requestSource := requestMetadataTokenSource{
		ctx:     context.Background(),
		client:  &failingMetadataClient{tokenErr: errors.New("metadata token unavailable")},
		account: testIdentity,
	}
	if _, err := requestSource.Token(); err == nil || !strings.Contains(err.Error(), "metadata token unavailable") {
		t.Fatalf("metadata token error = %v", err)
	}
}

func TestImpersonatedConfigurationRejectsEveryAuthorityAndShapeDrift(t *testing.T) {
	valid := validImpersonatedCarrier(testIdentity)
	for _, tt := range []struct {
		name string
		raw  string
		want string
	}{
		{name: "wrong top-level type", raw: strings.Replace(valid, `"type": "impersonated_service_account"`, `"type": "authorized_user"`, 1), want: "top-level type"},
		{name: "invalid URL", raw: strings.Replace(valid, "https://iamcredentials.googleapis.com", "https://%", 1), want: "service_account_impersonation_url"},
		{name: "wrong scheme", raw: strings.Replace(valid, "https://iamcredentials.googleapis.com", "http://iamcredentials.googleapis.com", 1), want: "canonical IAMCredentials authority"},
		{name: "URL user", raw: strings.Replace(valid, "https://iamcredentials.googleapis.com", "https://person@iamcredentials.googleapis.com", 1), want: "canonical IAMCredentials authority"},
		{name: "URL query", raw: strings.Replace(valid, ":generateAccessToken", ":generateAccessToken?fallback=true", 1), want: "canonical IAMCredentials authority"},
		{name: "URL fragment", raw: strings.Replace(valid, ":generateAccessToken", ":generateAccessToken#fallback", 1), want: "canonical IAMCredentials authority"},
		{name: "quota mismatch", raw: strings.Replace(valid, `"quota_project_id": "fixture-project"`, `"quota_project_id": "other-project"`, 1), want: "must match"},
		{name: "quota not string", raw: strings.Replace(valid, `"quota_project_id": "fixture-project"`, `"quota_project_id": 7`, 1), want: "must match"},
		{name: "source missing", raw: strings.Replace(valid, `"source_credentials": {`, `"different_credentials": {`, 1), want: "not allowed"},
		{name: "source not object", raw: strings.Replace(valid, `"source_credentials": {`, `"source_credentials_value": {`, 1), want: "not allowed"},
		{name: "missing client id", raw: strings.Replace(valid, `"client_id": "fixture-client",`, "", 1), want: "client_id is required"},
		{name: "blank client secret", raw: strings.Replace(valid, `"client_secret": "fixture-secret"`, `"client_secret": " "`, 1), want: "client_secret is required"},
		{name: "refresh token not string", raw: strings.Replace(valid, `"refresh_token": "fixture-refresh"`, `"refresh_token": 9`, 1), want: "refresh_token is required"},
		{name: "wrong token URI", raw: strings.Replace(valid, "https://oauth2.googleapis.com/token", "https://example.invalid/token", 1), want: "token_uri must equal"},
		{name: "token URI not string", raw: strings.Replace(valid, `"token_uri": "https://oauth2.googleapis.com/token"`, `"token_uri": 3`, 1), want: "token_uri must equal"},
		{name: "wrong universe", raw: strings.Replace(valid, `"universe_domain": "googleapis.com"`, `"universe_domain": "example.invalid"`, 1), want: "universe_domain must equal"},
		{name: "universe not string", raw: strings.Replace(valid, `"universe_domain": "googleapis.com"`, `"universe_domain": false`, 1), want: "universe_domain must equal"},
		{name: "rapt token not string", raw: strings.Replace(valid, `"universe_domain": "googleapis.com"`, `"universe_domain": "googleapis.com", "rapt_token": 2`, 1), want: "rapt_token must be a string"},
		{name: "account not string", raw: strings.Replace(valid, `"universe_domain": "googleapis.com"`, `"universe_domain": "googleapis.com", "account": true`, 1), want: "account must be a string"},
		{name: "delegates not array", raw: strings.Replace(valid, `"delegates": []`, `"delegates": {}`, 1), want: "delegates must be an empty array"},
		{name: "scopes not array", raw: strings.Replace(valid, `"scopes": ["`+cloudPlatformScopeFixture+`"]`, `"scopes": "`+cloudPlatformScopeFixture+`"`, 1), want: "scopes must contain only"},
		{name: "multiple scopes", raw: strings.Replace(valid, `"scopes": ["`+cloudPlatformScopeFixture+`"]`, `"scopes": ["`+cloudPlatformScopeFixture+`", "extra"]`, 1), want: "scopes must contain only"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := validateImpersonatedConfiguration([]byte(tt.raw), testIdentity); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestCredentialJSONScannerCoversScalarArrayAndMalformedContainers(t *testing.T) {
	for _, raw := range []string{`null`, `true`, `42`, `"value"`, `[]`, `[1, {"nested": [true, null]}]`} {
		if err := rejectDuplicateJSONKeys([]byte(raw)); err != nil {
			t.Fatalf("rejectDuplicateJSONKeys(%s) = %v", raw, err)
		}
	}
	for _, raw := range []string{`{"key":`, `{"key": 1`, `[1`, `[}`} {
		if err := rejectDuplicateJSONKeys([]byte(raw)); err == nil {
			t.Fatalf("rejectDuplicateJSONKeys(%s) accepted malformed JSON", raw)
		}
	}
	if got := serviceAccountProject("not-an-identity"); got != "" {
		t.Fatalf("invalid service-account project = %q", got)
	}
}
