package config

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func validConfig(t *testing.T) (string, string) {
	t.Helper()
	base := t.TempDir()
	state := filepath.Join(base, "state")
	checkout := filepath.Join(base, "checkout")
	if err := os.Mkdir(state, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(checkout, 0o700); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(checkout, "history-ragd")
	if err := os.WriteFile(executable, []byte("candidate"), 0o700); err != nil {
		t.Fatal(err)
	}
	body := `{"state_dir":` + quote(state) + `,"listen":"127.0.0.1:4680","pid_file":` + quote(filepath.Join(state, "daemon.pid")) + `,"auth_state_file":` + quote(filepath.Join(state, "auth.json")) + `,"auth_enabled":true,"checkout_root":` + quote(checkout) + `,"executable":` + quote(executable) + `}`
	path := filepath.Join(base, "config.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path, body
}

func quote(value string) string {
	return `"` + strings.ReplaceAll(value, `\`, `\\`) + `"`
}

func TestLoadClosedAndSafe(t *testing.T) {
	path, _ := validConfig(t)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Listen != "127.0.0.1:4680" || !cfg.AuthEnabled {
		t.Fatalf("unexpected config: %#v", cfg)
	}
}

func TestReadOnlyModeCannotStartWatcherOrDisableAuth(t *testing.T) {
	path, body := validConfig(t)
	candidate := strings.Replace(body, `"listen":"127.0.0.1:4680"`, `"listen":"127.0.0.1:4681","read_only":true`, 1)
	if err := os.WriteFile(path, []byte(candidate), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{strings.Replace(candidate, `"auth_enabled":true`, `"auth_enabled":false`, 1), strings.Replace(candidate, `"read_only":true`, `"read_only":true,"watch_roots":["/sources"]`, 1), strings.Replace(candidate, "127.0.0.1:4681", "0.0.0.0:4681", 1)} {
		if err := os.WriteFile(path, []byte(bad), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path); err == nil {
			t.Fatal("unsafe read-only configuration accepted")
		}
	}
}

func TestLoadRejectsUnknownDuplicateOversizedAndTrailingData(t *testing.T) {
	path, body := validConfig(t)
	cases := map[string]string{
		"unknown":   strings.Replace(body, `}`, `,"surprise":true}`, 1),
		"duplicate": strings.Replace(body, `"listen":"127.0.0.1:4680"`, `"listen":"127.0.0.1:4680","listen":"127.0.0.1:4681"`, 1),
		"trailing":  body + `{}`,
		"oversized": body + strings.Repeat(" ", int(MaxConfigBytes)),
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path); err == nil {
				t.Fatal("Load() accepted ambiguous or unbounded config")
			}
		})
	}
}

func TestLoadRejectsUnsafePathsNonLoopbackAndSymlinkConfig(t *testing.T) {
	path, body := validConfig(t)
	stateDir := filepath.Dir(filepath.Join(filepath.Dir(path), "state", "daemon.pid"))
	cases := map[string]string{
		"pid outside state": strings.Replace(body, quote(filepath.Join(stateDir, "daemon.pid")), quote(filepath.Join(filepath.Dir(stateDir), "outside.pid")), 1),
		"non loopback":      strings.Replace(body, "127.0.0.1:4680", "0.0.0.0:4680", 1),
		"relative state":    strings.Replace(body, quote(stateDir), quote("relative-state"), 1),
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path); err == nil {
				t.Fatal("Load() accepted unsafe config")
			}
		})
	}

	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(filepath.Dir(path), "config-link.json")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(link); err == nil {
		t.Fatal("Load() followed a config symlink")
	}
	hardlink := filepath.Join(filepath.Dir(path), "config-hardlink.json")
	if err := os.Link(path, hardlink); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("Load() accepted a multiply-linked config")
	}
}

func TestContainerModeRequiresAuthenticatedFixedPublicBind(t *testing.T) {
	path, body := validConfig(t)
	validContainer := strings.Replace(body, `"listen":"127.0.0.1:4680"`, `"listen":"0.0.0.0:4680","container_mode":true,"watch_roots":["/watch/claude","/watch/codex","/watch/gemini","/watch/antigravity","/watch/chatgpt","/watch/claude-app"]`, 1)
	if err := os.WriteFile(path, []byte(validContainer), 0o600); err != nil {
		t.Fatal(err)
	}
	if cfg, err := Load(path); err != nil || !cfg.ContainerMode || cfg.Listen != "0.0.0.0:4680" {
		t.Fatalf("Load(container) = %#v, %v", cfg, err)
	}
	for name, value := range map[string]string{
		"unauthenticated": strings.Replace(validContainer, `"auth_enabled":true`, `"auth_enabled":false`, 1),
		"loopback":        strings.Replace(validContainer, "0.0.0.0:4680", "127.0.0.1:4680", 1),
		"duplicate roots": strings.Replace(validContainer, `"/watch/codex"`, `"/watch/claude"`, 1),
		"missing roots":   strings.Replace(validContainer, `,"watch_roots":["/watch/claude","/watch/codex","/watch/gemini","/watch/antigravity","/watch/chatgpt","/watch/claude-app"]`, "", 1),
		"relative root":   strings.Replace(validContainer, `"/watch/claude"`, `"relative"`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path); err == nil {
				t.Fatal("Load accepted unsafe container bind")
			}
		})
	}
}

func TestJSONWalkerAndEOFRejectMalformedNestedValues(t *testing.T) {
	for name, payload := range map[string]string{
		"nested object duplicate": `{"outer":{"name":1,"name":2}}`,
		"nested array duplicate":  `[{"name":1,"name":2}]`,
		"unterminated object":     `{`,
		"trailing value":          `{} {}`,
	} {
		t.Run(name, func(t *testing.T) {
			if err := rejectDuplicateKeys([]byte(payload)); err == nil {
				t.Fatalf("rejectDuplicateKeys(%q) accepted malformed input", payload)
			}
		})
	}
	if err := rejectDuplicateKeys([]byte(`{"outer":[true,null,{"count":2}]}`)); err != nil {
		t.Fatalf("rejectDuplicateKeys(valid nested value) = %v", err)
	}
	decoder := json.NewDecoder(bytes.NewBufferString(`{} {}`))
	if err := expectEOF(decoder); err == nil {
		t.Fatal("expectEOF accepted a second JSON value")
	}
}

func TestValidateCanonicalizesAndRejectsUnsafeFilesystemInputs(t *testing.T) {
	path, _ := validConfig(t)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Config){
		"wrong port":          func(value *Config) { value.Listen = "127.0.0.1:4681" },
		"relative executable": func(value *Config) { value.Executable = "history-ragd" },
		"same durable files":  func(value *Config) { value.AuthStateFile = value.PIDFile },
		"outside executable":  func(value *Config) { value.Executable = path },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := cfg
			mutate(&candidate)
			if err := candidate.validate(); err == nil {
				t.Fatal("validate accepted unsafe configuration")
			}
		})
	}
	if err := os.Chmod(cfg.StateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := cfg.validate(); err == nil {
		t.Fatal("validate accepted a non-owner-only state directory")
	}
}

func TestOwnerOnlyConfigFileAndLinkChecks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := requireOwnerOnlyConfigMode(info); err != nil {
		t.Fatalf("requireOwnerOnlyConfigMode(0600) = %v", err)
	}
	if err := requireSingleLink(path); err != nil {
		t.Fatalf("requireSingleLink(single) = %v", err)
	}
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	info, err = os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := requireOwnerOnlyConfigMode(info); err == nil {
		t.Fatal("requireOwnerOnlyConfigMode accepted group-readable file")
	}
	if err := requireSingleLink(path + ".missing"); err == nil {
		t.Fatal("requireSingleLink accepted a missing path")
	}
}
