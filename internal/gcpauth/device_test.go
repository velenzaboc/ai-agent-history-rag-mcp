package gcpauth

import (
	"encoding/json"
	"strings"
	"testing"
)

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
