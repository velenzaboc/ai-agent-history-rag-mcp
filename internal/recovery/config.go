package recovery

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const maxConfigBytes int64 = 256 << 10

var (
	identifierPattern  = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	environmentPattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,127}$`)
	colorPattern       = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)
)

type Config struct {
	SchemaVersion          int                  `json:"schema_version"`
	Listen                 string               `json:"listen"`
	BasePath               string               `json:"base_path"`
	RequestTimeoutSeconds  int                  `json:"request_timeout_seconds"`
	ShutdownTimeoutSeconds int                  `json:"shutdown_timeout_seconds"`
	Access                 AccessConfig         `json:"access"`
	Fleet                  FleetConfig          `json:"fleet"`
	History                HistoryConfig        `json:"history"`
	Matching               MatchingConfig       `json:"matching"`
	Classification         ClassificationConfig `json:"classification"`
	Status                 StatusConfig         `json:"status"`
	View                   ViewConfig           `json:"view"`
}

type AccessConfig struct {
	Mode      string `json:"mode"`
	BearerEnv string `json:"bearer_env"`
}

type FleetConfig struct {
	Mode             string `json:"mode"`
	Endpoint         string `json:"endpoint"`
	ProjectID        string `json:"project_id"`
	SnapshotTool     string `json:"snapshot_tool"`
	TasksTool        string `json:"tasks_tool"`
	WorklinksTool    string `json:"worklinks_tool"`
	Scope            string `json:"scope"`
	RootTaskID       string `json:"root_task_id"`
	Limit            int    `json:"limit"`
	ActiveLimit      int    `json:"active_limit"`
	MaxResponseBytes int64  `json:"max_response_bytes"`
}

type HistoryConfig struct {
	Mode              string              `json:"mode"`
	Endpoint          string              `json:"endpoint"`
	StatusPath        string              `json:"status_path"`
	SessionsPath      string              `json:"sessions_path"`
	SearchPath        string              `json:"search_path"`
	FilesPath         string              `json:"files_path"`
	Auth              UpstreamAuthConfig  `json:"auth"`
	RecentLimit       int                 `json:"recent_limit"`
	SearchLimit       int                 `json:"search_limit"`
	SessionCacheLimit int                 `json:"session_cache_limit"`
	ProbeLimit        int                 `json:"probe_limit"`
	ProbeConcurrency  int                 `json:"probe_concurrency"`
	MaxResponseBytes  int64               `json:"max_response_bytes"`
	ProjectFilter     string              `json:"project_filter"`
	Search            HistorySearchConfig `json:"search"`
}

type HistorySearchConfig struct {
	UseHybrid       bool `json:"use_hybrid"`
	EnableAnalysis  bool `json:"enable_analysis"`
	EnableSynthesis bool `json:"enable_synthesis"`
	IncludeDebug    bool `json:"include_debug"`
}

type UpstreamAuthConfig struct {
	Mode      string `json:"mode"`
	BearerEnv string `json:"bearer_env"`
}

type MatchingConfig struct {
	SessionIDPattern string         `json:"session_id_pattern"`
	PathRewrites     []PathRewrite  `json:"path_rewrites"`
	Weights          map[string]int `json:"weights"`
	AcceptScore      int            `json:"accept_score"`
	SuggestScore     int            `json:"suggest_score"`
}

type PathRewrite struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type ClassificationConfig struct {
	StaleAfterHours     int                  `json:"stale_after_hours"`
	AbandonedAfterHours int                  `json:"abandoned_after_hours"`
	Rules               []ClassificationRule `json:"rules"`
}

type ClassificationRule struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Severity string `json:"severity"`
	Color    string `json:"color"`
}

type StatusConfig struct {
	Groups           []StatusGroup `json:"groups"`
	ActiveGroupIDs   []string      `json:"active_group_ids"`
	TerminalGroupIDs []string      `json:"terminal_group_ids"`
	MilestoneLevels  []string      `json:"milestone_levels"`
}

type StatusGroup struct {
	ID       string   `json:"id"`
	Label    string   `json:"label"`
	Color    string   `json:"color"`
	Statuses []string `json:"statuses"`
}

type ViewConfig struct {
	Title                    string            `json:"title"`
	Eyebrow                  string            `json:"eyebrow"`
	Subtitle                 string            `json:"subtitle"`
	RefreshSeconds           int               `json:"refresh_seconds"`
	CacheSeconds             int               `json:"cache_seconds"`
	MaxAPIResponseBytes      int64             `json:"max_api_response_bytes"`
	MaxLaneCards             int               `json:"max_lane_cards"`
	MaxGraphNodes            int               `json:"max_graph_nodes"`
	SummaryPreviewCharacters int               `json:"summary_preview_characters"`
	Labels                   map[string]string `json:"labels"`
	Theme                    ThemeConfig       `json:"theme"`
}

type ThemeConfig struct {
	Background string `json:"background"`
	Surface    string `json:"surface"`
	SurfaceAlt string `json:"surface_alt"`
	Text       string `json:"text"`
	Muted      string `json:"muted"`
	Accent     string `json:"accent"`
	AccentAlt  string `json:"accent_alt"`
	Danger     string `json:"danger"`
	Warning    string `json:"warning"`
	Success    string `json:"success"`
}

func LoadConfig(configPath string) (Config, error) {
	if !filepath.IsAbs(configPath) || filepath.Clean(configPath) != configPath {
		return Config{}, errors.New("config path must be absolute and clean")
	}
	info, err := os.Lstat(configPath)
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return Config{}, errors.New("config must be a regular non-link file")
	}
	file, err := os.Open(configPath)
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	defer file.Close()
	payload, err := io.ReadAll(io.LimitReader(file, maxConfigBytes+1))
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	if int64(len(payload)) > maxConfigBytes {
		return Config{}, errors.New("config exceeds size limit")
	}
	if err := rejectDuplicateJSONKeys(payload); err != nil {
		return Config{}, fmt.Errorf("decode config: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var cfg Config
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("decode config: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return Config{}, errors.New("decode config: trailing JSON value")
	}
	if err := cfg.validate(); err != nil {
		return Config{}, fmt.Errorf("validate config: %w", err)
	}
	return cfg, nil
}

func (cfg Config) RequestTimeout() time.Duration {
	return time.Duration(cfg.RequestTimeoutSeconds) * time.Second
}

func (cfg Config) ShutdownTimeout() time.Duration {
	return time.Duration(cfg.ShutdownTimeoutSeconds) * time.Second
}

func (cfg Config) validate() error {
	if cfg.SchemaVersion != 1 {
		return errors.New("schema_version must equal 1")
	}
	host, port, err := net.SplitHostPort(cfg.Listen)
	if err != nil || net.ParseIP(host) == nil || port == "" {
		return errors.New("listen must be a numeric IP endpoint")
	}
	if cfg.Access.Mode == "none" && !net.ParseIP(host).IsLoopback() {
		return errors.New("access mode none requires a loopback listen address")
	}
	switch cfg.Access.Mode {
	case "none":
		if cfg.Access.BearerEnv != "" {
			return errors.New("access bearer_env must be empty when mode is none")
		}
	case "bearer_env":
		if !environmentPattern.MatchString(cfg.Access.BearerEnv) {
			return errors.New("access bearer_env is invalid")
		}
	default:
		return errors.New("access mode must be none or bearer_env")
	}
	if cfg.BasePath == "" || cfg.BasePath[0] != '/' || path.Clean(cfg.BasePath) != cfg.BasePath || strings.Contains(cfg.BasePath, "//") {
		return errors.New("base_path must be an absolute clean URL path")
	}
	if cfg.RequestTimeoutSeconds < 1 || cfg.RequestTimeoutSeconds > 300 || cfg.ShutdownTimeoutSeconds < 1 || cfg.ShutdownTimeoutSeconds > 300 {
		return errors.New("timeout values must be between 1 and 300 seconds")
	}
	if err := cfg.Fleet.validate(); err != nil {
		return fmt.Errorf("fleet: %w", err)
	}
	if err := cfg.History.validate(); err != nil {
		return fmt.Errorf("history: %w", err)
	}
	if err := cfg.Matching.validate(); err != nil {
		return fmt.Errorf("matching: %w", err)
	}
	if err := cfg.Classification.validate(); err != nil {
		return fmt.Errorf("classification: %w", err)
	}
	if err := cfg.Status.validate(); err != nil {
		return fmt.Errorf("status: %w", err)
	}
	if cfg.Fleet.TasksTool != "" && len(cfg.Status.ActiveStatuses()) == 0 {
		return errors.New("fleet active overlay requires at least one active status")
	}
	if err := cfg.View.validate(); err != nil {
		return fmt.Errorf("view: %w", err)
	}
	return nil
}

func (cfg FleetConfig) validate() error {
	if cfg.Mode != "mcp_http" {
		return errors.New("mode must be mcp_http")
	}
	if err := validateEndpoint(cfg.Endpoint); err != nil {
		return err
	}
	if strings.TrimSpace(cfg.ProjectID) == "" || strings.TrimSpace(cfg.SnapshotTool) == "" || strings.TrimSpace(cfg.WorklinksTool) == "" || strings.TrimSpace(cfg.Scope) == "" {
		return errors.New("project_id, snapshot_tool, worklinks_tool, and scope are required")
	}
	if !identifierPattern.MatchString(cfg.SnapshotTool) || !identifierPattern.MatchString(cfg.WorklinksTool) {
		return errors.New("snapshot_tool or worklinks_tool is invalid")
	}
	if cfg.Limit < 1 || cfg.Limit > 100000 {
		return errors.New("limit must be between 1 and 100000")
	}
	if cfg.TasksTool == "" {
		if cfg.ActiveLimit != 0 {
			return errors.New("active_limit must be zero when tasks_tool is disabled")
		}
	} else if !identifierPattern.MatchString(cfg.TasksTool) || cfg.ActiveLimit < 1 || cfg.ActiveLimit > 100000 {
		return errors.New("tasks_tool and active_limit must define a valid active-task overlay")
	}
	return validateResponseLimit(cfg.MaxResponseBytes)
}

func (cfg HistoryConfig) validate() error {
	if cfg.Mode != "legacy_http" {
		return errors.New("mode must be legacy_http")
	}
	if err := validateEndpoint(cfg.Endpoint); err != nil {
		return err
	}
	for label, value := range map[string]string{"status_path": cfg.StatusPath, "sessions_path": cfg.SessionsPath, "search_path": cfg.SearchPath, "files_path": cfg.FilesPath} {
		if value == "" || value[0] != '/' {
			return fmt.Errorf("%s must be an absolute URL path", label)
		}
	}
	switch cfg.Auth.Mode {
	case "none":
		if cfg.Auth.BearerEnv != "" {
			return errors.New("auth bearer_env must be empty when mode is none")
		}
	case "bearer_env":
		if !environmentPattern.MatchString(cfg.Auth.BearerEnv) {
			return errors.New("auth bearer_env is invalid")
		}
	default:
		return errors.New("auth mode must be none or bearer_env")
	}
	if cfg.RecentLimit < 1 || cfg.RecentLimit > 1000 || cfg.SearchLimit < 1 || cfg.SearchLimit > 50 || cfg.SessionCacheLimit < cfg.RecentLimit || cfg.SessionCacheLimit > 10000 || cfg.ProbeLimit < 0 || cfg.ProbeLimit > 10000 || cfg.ProbeConcurrency < 1 || cfg.ProbeConcurrency > 32 {
		return errors.New("history limits are outside the supported bounds")
	}
	return validateResponseLimit(cfg.MaxResponseBytes)
}

func validateEndpoint(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.User != nil || parsed.Fragment != "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return errors.New("endpoint must be an absolute HTTP(S) URL without userinfo or fragment")
	}
	if parsed.Scheme == "http" {
		host := parsed.Hostname()
		ip := net.ParseIP(host)
		if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return errors.New("plain HTTP endpoint must be loopback")
		}
	}
	return nil
}

func validateResponseLimit(value int64) error {
	if value < 1<<10 || value > 128<<20 {
		return errors.New("max_response_bytes must be between 1 KiB and 128 MiB")
	}
	return nil
}

func (cfg MatchingConfig) validate() error {
	if _, err := regexp.Compile(cfg.SessionIDPattern); err != nil || cfg.SessionIDPattern == "" {
		return errors.New("session_id_pattern must be a valid non-empty expression")
	}
	for _, rewrite := range cfg.PathRewrites {
		if rewrite.From == "" || rewrite.To == "" {
			return errors.New("path rewrites require non-empty from and to values")
		}
	}
	for _, key := range []string{"exact_session", "exact_path", "project_name", "title_token"} {
		if cfg.Weights[key] < 0 {
			return fmt.Errorf("weight %s must be non-negative", key)
		}
	}
	if cfg.SuggestScore < 1 || cfg.AcceptScore < cfg.SuggestScore || cfg.Weights["exact_session"] < cfg.AcceptScore {
		return errors.New("matching score thresholds are inconsistent")
	}
	return nil
}

func (cfg ClassificationConfig) validate() error {
	if cfg.StaleAfterHours < 1 || cfg.AbandonedAfterHours < cfg.StaleAfterHours {
		return errors.New("abandoned_after_hours must be at least stale_after_hours")
	}
	seen := make(map[string]struct{})
	for _, rule := range cfg.Rules {
		if !identifierPattern.MatchString(rule.ID) || strings.TrimSpace(rule.Label) == "" || !colorPattern.MatchString(rule.Color) {
			return errors.New("classification rule contains an invalid id, label, or color")
		}
		if rule.Severity != "low" && rule.Severity != "medium" && rule.Severity != "high" {
			return errors.New("classification rule severity must be low, medium, or high")
		}
		if _, exists := seen[rule.ID]; exists {
			return errors.New("classification rule ids must be unique")
		}
		seen[rule.ID] = struct{}{}
	}
	for _, required := range []string{"orphan_session", "abandoned_session", "stale_thread", "missing_history", "ambiguous_binding", "unlinked_task"} {
		if _, ok := seen[required]; !ok {
			return fmt.Errorf("classification rule %s is required", required)
		}
	}
	return nil
}

func (cfg StatusConfig) validate() error {
	groups := make(map[string]struct{})
	statuses := make(map[string]struct{})
	for _, group := range cfg.Groups {
		if !identifierPattern.MatchString(group.ID) || strings.TrimSpace(group.Label) == "" || !colorPattern.MatchString(group.Color) || len(group.Statuses) == 0 {
			return errors.New("status group contains an invalid id, label, color, or empty status set")
		}
		if _, exists := groups[group.ID]; exists {
			return errors.New("status group ids must be unique")
		}
		groups[group.ID] = struct{}{}
		for _, status := range group.Statuses {
			if strings.TrimSpace(status) == "" {
				return errors.New("status values must be non-empty")
			}
			if _, exists := statuses[status]; exists {
				return errors.New("each status may belong to only one group")
			}
			statuses[status] = struct{}{}
		}
	}
	if len(groups) == 0 {
		return errors.New("at least one status group is required")
	}
	for _, id := range append(append([]string(nil), cfg.ActiveGroupIDs...), cfg.TerminalGroupIDs...) {
		if _, ok := groups[id]; !ok {
			return fmt.Errorf("status group reference %s is not registered", id)
		}
	}
	return nil
}

func (cfg StatusConfig) ActiveStatuses() []string {
	activeGroups := make(map[string]struct{}, len(cfg.ActiveGroupIDs))
	for _, id := range cfg.ActiveGroupIDs {
		activeGroups[id] = struct{}{}
	}
	statuses := make([]string, 0)
	for _, group := range cfg.Groups {
		if _, active := activeGroups[group.ID]; active {
			statuses = append(statuses, group.Statuses...)
		}
	}
	return statuses
}

func (cfg ViewConfig) validate() error {
	if strings.TrimSpace(cfg.Title) == "" || strings.TrimSpace(cfg.Eyebrow) == "" || strings.TrimSpace(cfg.Subtitle) == "" {
		return errors.New("title, eyebrow, and subtitle are required")
	}
	if cfg.RefreshSeconds < 5 || cfg.RefreshSeconds > 3600 || cfg.CacheSeconds < 0 || cfg.CacheSeconds > cfg.RefreshSeconds || cfg.MaxLaneCards < 1 || cfg.MaxGraphNodes < 1 || cfg.SummaryPreviewCharacters < 40 || cfg.SummaryPreviewCharacters > 10000 {
		return errors.New("view limits are outside the supported bounds")
	}
	if err := validateResponseLimit(cfg.MaxAPIResponseBytes); err != nil {
		return fmt.Errorf("max_api_response_bytes: %w", err)
	}
	for _, value := range []string{cfg.Theme.Background, cfg.Theme.Surface, cfg.Theme.SurfaceAlt, cfg.Theme.Text, cfg.Theme.Muted, cfg.Theme.Accent, cfg.Theme.AccentAlt, cfg.Theme.Danger, cfg.Theme.Warning, cfg.Theme.Success} {
		if !colorPattern.MatchString(value) {
			return errors.New("theme colors must be six-digit hex values")
		}
	}
	for _, key := range []string{"inbox", "lanes", "tasks", "milestones", "graph", "history_search", "copy_resume", "sources"} {
		if strings.TrimSpace(cfg.Labels[key]) == "" {
			return fmt.Errorf("view label %s is required", key)
		}
	}
	return nil
}

func rejectDuplicateJSONKeys(payload []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	if err := walkJSON(decoder); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON value")
	}
	return nil
}

func walkJSON(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("object key must be a string")
			}
			if _, exists := seen[key]; exists {
				return fmt.Errorf("duplicate object key %q", key)
			}
			seen[key] = struct{}{}
			if err := walkJSON(decoder); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	case '[':
		for decoder.More() {
			if err := walkJSON(decoder); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	default:
		return errors.New("unexpected JSON delimiter")
	}
}
