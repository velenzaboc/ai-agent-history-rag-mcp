package gcpauth

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/oauth2"
)

func TestDeviceTokenSourceUsesPrivateCarrierWithoutChangingIt(t *testing.T) {
	clearAmbientGoogleEnvironment(t)
	path := filepath.Join(t.TempDir(), "adc.json")
	raw := []byte(deviceSourceCarrier(testIdentity))
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	restrictTestFileAccess(t, path)
	previous := defaultADCPath
	defaultADCPath = func() (string, error) { return path, nil }
	t.Cleanup(func() { defaultADCPath = previous })
	selector := Selector{CredentialsSource: CredentialsSourceApplicationDefault, CredentialsProfile: CredentialsProfileDeviceServiceAccount, CredentialsIdentity: "device-source@fixture-project.iam.gserviceaccount.com"}
	ctx := context.WithValue(context.Background(), oauth2.HTTPClient, &http.Client{})
	for _, factory := range []func(context.Context, ...string) (oauth2.TokenSource, error){selector.TokenSource, selector.TokenSourceForRequest} {
		source, err := factory(ctx)
		if err != nil || source == nil {
			t.Fatalf("device token source construction: %v", err)
		}
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(raw) {
		t.Fatal("credential carrier changed")
	}
	selector.CredentialsIdentity = testIdentity
	if _, err := selector.TokenSource(ctx); err == nil {
		t.Fatal("carrier target accepted as device identity")
	}
	selector.CredentialsIdentity = "device-source@fixture-project.iam.gserviceaccount.com"
	for _, key := range []string{"GOOGLE_APPLICATION_CREDENTIALS", "CLOUDSDK_AUTH_CREDENTIAL_FILE_OVERRIDE", "CLOUDSDK_CONFIG"} {
		t.Run(key, func(t *testing.T) {
			t.Setenv(key, path)
			if _, err := selector.TokenSource(ctx); err == nil {
				t.Fatal("ambient credential override accepted")
			}
		})
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := selector.TokenSource(ctx); err == nil {
		t.Fatal("missing carrier accepted")
	}
	defaultADCPath = func() (string, error) { return "", os.ErrPermission }
	if _, err := selector.TokenSource(ctx); err == nil {
		t.Fatal("carrier resolution failure ignored")
	}
}

func TestDeviceSourceUsesExactExistingNestedIdentityWithoutCarrierMutation(t *testing.T) {
	raw := deviceSourceCarrier(testIdentity)
	raw = strings.Replace(raw, ",\n  \"quota_project_id\": \"fixture-project\"", "", 1)
	before := raw
	data, err := deviceSourceConfiguration([]byte(raw), "device-source@fixture-project.iam.gserviceaccount.com")
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err = json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	if result["type"] != "service_account" || result["client_email"] != "device-source@fixture-project.iam.gserviceaccount.com" || raw != before {
		t.Fatal("wrong identity or carrier changed")
	}
	for _, candidate := range []string{validImpersonatedCarrier(testIdentity), strings.Replace(raw, "https://oauth2.googleapis.com/token", "https://invalid.example/token", 1), strings.Replace(raw, `"delegates": []`, `"delegates": [], "delegates": []`, 1)} {
		if _, err = deviceSourceConfiguration([]byte(candidate), "device-source@fixture-project.iam.gserviceaccount.com"); err == nil {
			t.Fatal("invalid device source accepted")
		}
	}
	if _, err = deviceSourceConfiguration([]byte(raw), testIdentity); err == nil {
		t.Fatal("target mistaken for source identity")
	}
}

func TestDeviceTokenSourceResolvesKopiaBackupCarrierForServiceAccountAndX509(t *testing.T) {
	f := newWorkloadFixture(t)
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "")
	if err := os.Unsetenv("GOOGLE_APPLICATION_CREDENTIALS"); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	backupDir := filepath.Join(home, ".config", "no13-backup")
	if err := os.MkdirAll(backupDir, 0o700); err != nil {
		t.Fatal(err)
	}

	// 1. Direct service_account carrier in ~/.config/no13-backup/kopia-macmini.json
	saEmail := "kopia-macmini@jeeves-486102.iam.gserviceaccount.com"
	saRaw, err := deviceSourceConfiguration([]byte(deviceSourceCarrier(testIdentity)), "device-source@fixture-project.iam.gserviceaccount.com")
	if err != nil {
		t.Fatal(err)
	}
	saJSON := strings.ReplaceAll(string(saRaw), "device-source@fixture-project.iam.gserviceaccount.com", saEmail)
	saJSON = strings.ReplaceAll(saJSON, "fixture-project", "jeeves-486102")
	saPath := filepath.Join(backupDir, "kopia-macmini.json")
	writeWorkloadBytes(t, saPath, []byte(saJSON))
	restrictTestFileAccess(t, saPath)

	saSelector := Selector{
		CredentialsSource:   CredentialsSourceApplicationDefault,
		CredentialsProfile:  CredentialsProfileDeviceServiceAccount,
		CredentialsIdentity: saEmail,
	}
	if _, err := saSelector.TokenSource(context.Background()); err != nil {
		t.Fatalf("direct kopia service_account carrier rejected: %v", err)
	}
	saSelector.CredentialsIdentity = "kopia-macbook@jeeves-486102.iam.gserviceaccount.com"
	if _, err := saSelector.TokenSource(context.Background()); err == nil {
		t.Fatal("mismatched kopia service_account identity accepted")
	}

	// 2. Direct external_account X.509 carrier in ~/.config/no13-backup/kopia-arcserver1.json
	wifEmail := "kopia-arcserver1@jeeves-486102.iam.gserviceaccount.com"
	f.carrier["service_account_impersonation_url"] = "https://iamcredentials.mtls.googleapis.com/v1/projects/-/serviceAccounts/" + wifEmail + ":generateAccessToken"
	wifPath := filepath.Join(backupDir, "kopia-arcserver1.json")
	writeWorkloadJSON(t, wifPath, f.carrier)
	restrictTestFileAccess(t, wifPath)

	wifSelector := Selector{
		CredentialsSource:   CredentialsSourceApplicationDefault,
		CredentialsProfile:  CredentialsProfileDeviceServiceAccount,
		CredentialsIdentity: wifEmail,
	}
	if _, err := wifSelector.TokenSource(context.Background()); err != nil {
		t.Fatalf("direct kopia external_account X.509 carrier rejected: %v", err)
	}
	f.carrier["service_account_impersonation_url"] = "https://iamcredentials.mtls.googleapis.com/v1/projects/-/serviceAccounts/kopia-arcserver4@jeeves-486102.iam.gserviceaccount.com:generateAccessToken"
	writeWorkloadJSON(t, wifPath, f.carrier)
	restrictTestFileAccess(t, wifPath)
	if _, err := wifSelector.TokenSource(context.Background()); err == nil {
		t.Fatal("mismatched kopia external_account identity accepted")
	}
}
