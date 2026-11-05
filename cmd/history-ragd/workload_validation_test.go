package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNativeWorkloadValidationCommandUsesProductionBoundaryWithoutStartingDaemon(t *testing.T) {
	for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy", "CLOUDSDK_CONFIG", "GOOGLE_API_CERTIFICATE_CONFIG", "GOOGLE_AUTH_TRUST_BOUNDARY_ENABLED", "GOOGLE_API_USE_CLIENT_CERTIFICATE", "GOOGLE_API_USE_MTLS_ENDPOINT"} {
		t.Setenv(key, "")
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
	}
	dir := t.TempDir()
	key, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(1003), Subject: pkix.Name{CommonName: "verification host"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	private, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	write := func(name string, data []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, 0600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	certPath := write("client.pem", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	keyPath := write("client.key", pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private}))
	jsonFile := func(name string, v any) string {
		data, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return write(name, data)
	}
	certConfig := jsonFile("certificate_config.json", map[string]any{"cert_configs": map[string]any{"workload": map[string]any{"cert_path": certPath, "key_path": keyPath}}})
	base := "device-builder@verification-project.iam.gserviceaccount.com"
	target := "history-reader@verification-project.iam.gserviceaccount.com"
	audience := "//iam.googleapis.com/projects/123456789012/locations/global/workloadIdentityPools/verification-device/providers/device-certificate"
	carrier := jsonFile("workload.json", map[string]any{"type": "external_account", "audience": audience, "subject_token_type": "urn:ietf:params:oauth:token-type:mtls", "token_url": "https://sts.mtls.googleapis.com/v1/token", "service_account_impersonation_url": "https://iamcredentials.mtls.googleapis.com/v1/projects/-/serviceAccounts/" + base + ":generateAccessToken", "credential_source": map[string]any{"certificate": map[string]any{"certificate_config_location": certConfig}}})
	for key, value := range map[string]string{"GOOGLE_APPLICATION_CREDENTIALS": carrier, "CLAUDE_HISTORY_RAG_RUNTIME_CONTRACT": "production", "CLAUDE_HISTORY_RAG_STORAGE_BACKEND": "spanner", "CLAUDE_HISTORY_RAG_SPANNER_PROJECT": "verification-project", "CLAUDE_HISTORY_RAG_SPANNER_INSTANCE": "verification-instance", "CLAUDE_HISTORY_RAG_SPANNER_DATABASE": "history-corpus", "CLAUDE_HISTORY_RAG_SPANNER_EMBEDDING_MODE": "spanner", "CLAUDE_HISTORY_RAG_SPANNER_EMBEDDING_MODEL_ID": "ConversationEmbeddingModel", "CLAUDE_HISTORY_RAG_EMBEDDING_PROVIDER": "vertex", "CLAUDE_HISTORY_RAG_EMBEDDING_MODEL": "gemini-embedding-001", "CLAUDE_HISTORY_RAG_EMBEDDING_DIMENSION": "3072", "CLAUDE_HISTORY_RAG_STATUS_SERVER_HOST": "127.0.0.1", "CLAUDE_HISTORY_RAG_STATUS_SERVER_PORT": "4681", "CLAUDE_HISTORY_RAG_READ_ONLY": "true", "CLAUDE_HISTORY_RAG_CREDENTIALS_SOURCE": "application_default", "CLAUDE_HISTORY_RAG_CREDENTIALS_PROFILE": "workload_identity_federation", "CLAUDE_HISTORY_RAG_CREDENTIALS_IDENTITY": target, "CLAUDE_HISTORY_RAG_CREDENTIALS_BASE_IDENTITY": base, "CLAUDE_HISTORY_RAG_CREDENTIALS_AUDIENCE": audience} {
		t.Setenv(key, value)
	}
	if err := run([]string{"validate-workload-credentials"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_HISTORY_RAG_CREDENTIALS_BASE_IDENTITY", target)
	if err := run([]string{"validate-workload-credentials"}); err == nil {
		t.Fatal("source equals target accepted")
	}
	t.Setenv("CLAUDE_HISTORY_RAG_CREDENTIALS_BASE_IDENTITY", base)
	t.Setenv("CLAUDE_HISTORY_RAG_STATUS_SERVER_PORT", "4680")
	if err := run([]string{"validate-workload-credentials"}); err == nil {
		t.Fatal("readonly endpoint substitution accepted")
	}
}
