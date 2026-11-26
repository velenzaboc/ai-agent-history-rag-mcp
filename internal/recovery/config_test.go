package recovery

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadConfigAcceptsClosedExplicitConfiguration(t *testing.T) {
	path := writeConfig(t, validConfigJSON())
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if cfg.Fleet.ProjectID != "project-a" || cfg.History.RecentLimit != 20 || cfg.Fleet.TaskPromptLimit != 250 || cfg.Fleet.SearchTool != "search" {
		t.Fatalf("unexpected config: %#v", cfg)
	}
	if !cfg.Discovery.Enabled || cfg.Discovery.CandidateLimit != 12 || len(cfg.Discovery.Verdicts) != 5 {
		t.Fatalf("related-work discovery was not preserved: %#v", cfg.Discovery)
	}
	if len(cfg.Programs) != 1 || cfg.Programs[0].ID != "delivery" || len(cfg.Programs[0].Stages) != 2 {
		t.Fatalf("program registry was not preserved: %#v", cfg.Programs)
	}
	if len(cfg.Status.Groups) != 3 || cfg.Status.Groups[0].ID != "queued" {
		t.Fatalf("status registry was not preserved: %#v", cfg.Status.Groups)
	}
	if cfg.Classification.SessionReview.ArtifactType != "finding" || len(cfg.Classification.SessionReview.Kinds) != 3 {
		t.Fatalf("session review registry was not preserved: %#v", cfg.Classification.SessionReview)
	}
}

func TestLoadConfigRejectsInconsistentDiscoveryThresholds(t *testing.T) {
	payload := strings.Replace(validConfigJSON(), `"related_score":30,"reuse_score":50`, `"related_score":60,"reuse_score":50`, 1)
	if _, err := LoadConfig(writeConfig(t, payload)); err == nil || !strings.Contains(err.Error(), "discovery") {
		t.Fatalf("LoadConfig() error = %v, want discovery threshold refusal", err)
	}
}

func TestLoadConfigRejectsInvalidSessionReviewRegistry(t *testing.T) {
	for name, payload := range map[string]string{
		"artifact type":  strings.Replace(validConfigJSON(), `"artifact_type":"finding"`, `"artifact_type":"not valid"`, 1),
		"duplicate kind": strings.Replace(validConfigJSON(), `"kinds":[`, `"kinds":[{"id":"superseded","label":"Duplicate","color":"#70e1c3"},`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := LoadConfig(writeConfig(t, payload)); err == nil || !strings.Contains(err.Error(), "session_review") {
				t.Fatalf("LoadConfig() error = %v, want session_review refusal", err)
			}
		})
	}
}

func TestExampleConfigStaysLoadable(t *testing.T) {
	path, err := filepath.Abs(filepath.Join("..", "..", "configs", "work-recovery-console.example.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err != nil {
		t.Fatalf("example configuration is invalid: %v", err)
	}
}

func TestLoadConfigRejectsUnknownAndDuplicateFields(t *testing.T) {
	for name, payload := range map[string]string{
		"unknown":   strings.Replace(validConfigJSON(), `"listen":`, `"surprise":true,"listen":`, 1),
		"duplicate": strings.Replace(validConfigJSON(), `"schema_version":1`, `"schema_version":1,"schema_version":1`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := LoadConfig(writeConfig(t, payload)); err == nil {
				t.Fatal("LoadConfig() accepted an invalid closed configuration")
			}
		})
	}
}

func TestConfigRequiresLoopbackWhenAccessIsDisabled(t *testing.T) {
	payload := strings.Replace(validConfigJSON(), `"127.0.0.1:4780"`, `"0.0.0.0:4780"`, 1)
	if _, err := LoadConfig(writeConfig(t, payload)); err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("LoadConfig() error = %v, want loopback refusal", err)
	}
}

func TestLoadConfigRejectsDuplicateProgramAndStageIDs(t *testing.T) {
	for name, payload := range map[string]string{
		"program": strings.Replace(validConfigJSON(), `"programs":[`, `"programs":[{"id":"delivery","nav_label":"Duplicate","title":"Duplicate","description":"Duplicate","root_task_id":"ROOT-2","lane_task_id_prefix":"LANE-","expected_lane_count":1,"limit":100,"source_thread_title":"","source_thread_id":"","source_note":"","checkpoint_ref":"","stages":[]},`, 1),
		"stage":   strings.Replace(validConfigJSON(), `"stages":[`, `"stages":[{"id":"plan","label":"Duplicate","task_id_prefix":"PLAN-2-"},`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := LoadConfig(writeConfig(t, payload)); err == nil {
				t.Fatal("LoadConfig() accepted duplicate program configuration")
			}
		})
	}
}

func writeConfig(t *testing.T, payload string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func validConfigJSON() string {
	return `{
  "schema_version":1,
  "listen":"127.0.0.1:4780",
  "base_path":"/",
  "request_timeout_seconds":15,
  "shutdown_timeout_seconds":10,
  "access":{"mode":"none","bearer_env":""},
  "fleet":{"mode":"mcp_http","endpoint":"http://127.0.0.1:8790/","project_id":"project-a","snapshot_tool":"snapshot","tasks_tool":"tasks","worklinks_tool":"worklinks","search_tool":"search","scope":"project","root_task_id":"","limit":500,"active_limit":100,"task_prompt_limit":250,"session_link_limit":10,"session_link_concurrency":4,"max_response_bytes":8388608},
  "history":{"mode":"legacy_http","endpoint":"http://127.0.0.1:4680","status_path":"/status?detail=basic","sessions_path":"/api/sessions","search_path":"/api/search","files_path":"/api/search/files","auth":{"mode":"bearer_env","bearer_env":"HISTORY_TOKEN"},"recent_limit":20,"search_limit":50,"session_cache_limit":500,"probe_limit":40,"probe_concurrency":4,"max_response_bytes":8388608,"project_filter":"","search":{"use_hybrid":true,"enable_analysis":false,"enable_synthesis":false,"include_debug":false}},
  "matching":{"session_id_pattern":"^[a-f0-9-]{36}$","path_rewrites":[],"weights":{"exact_session":100,"exact_path":80,"project_name":35,"title_token":10},"accept_score":70,"suggest_score":30},
  "discovery":{"enabled":true,"candidate_limit":12,"hydrate_limit":8,"hydrate_concurrency":4,"history_limit":8,"search_conversations":true,"search_files":true,"max_query_terms":8,"max_note_overlap_terms":4,"min_token_length":3,"query_fields":["title","note","pillar","repo","task_id"],"stop_words":["and","build","create","for","implement","implementation","read","task","the","this","with","work"],"weights":{"search_hit":0,"title_token_overlap":15,"note_token_overlap":2,"same_repo":15,"same_parent":20,"same_pillar":10,"shared_artifact":100,"terminal":20},"thresholds":{"related_score":30,"reuse_score":50},"verdicts":[{"id":"resume","label":"Resume existing","color":"#70e1c3"},{"id":"collision","label":"Binding collision","color":"#ff7a6b"},{"id":"reuse","label":"Reuse prior work","color":"#7aa2ff"},{"id":"related","label":"Related work","color":"#e8ad55"},{"id":"new","label":"No prior work","color":"#98a3b7"}]},
  "classification":{"stale_after_hours":168,"abandoned_after_hours":720,"session_review":{"artifact_type":"finding","artifact_ref_prefix":"recovery-disposition:","kinds":[{"id":"superseded","label":"Reviewed · superseded","color":"#70e1c3"},{"id":"unresolved","label":"Reviewed · identity unresolved","color":"#e8ad55"},{"id":"out_of_scope","label":"Reviewed · unrelated product","color":"#98a3b7"}]},"rules":[{"id":"orphan_session","label":"Unlinked session","severity":"medium","color":"#e8ad55"},{"id":"abandoned_session","label":"Abandoned session","severity":"high","color":"#ff7a6b"},{"id":"stale_thread","label":"Stale thread","severity":"high","color":"#ff7a6b"},{"id":"missing_history","label":"Missing history","severity":"high","color":"#ff7a6b"},{"id":"ambiguous_binding","label":"Ambiguous binding","severity":"medium","color":"#e8ad55"},{"id":"unlinked_task","label":"Task has no work link","severity":"low","color":"#7aa2ff"}]},
  "programs":[{"id":"delivery","nav_label":"Delivery","title":"Delivery program","description":"Live delivery graph","root_task_id":"ROOT-1","lane_task_id_prefix":"LANE-","expected_lane_count":2,"limit":500,"source_thread_title":"Handover","source_thread_id":"session-1","source_note":"Live source","checkpoint_ref":"checkpoint-1","stages":[{"id":"plan","label":"Plan","task_id_prefix":"PLAN-"},{"id":"validate","label":"Validate","task_id_prefix":"VALIDATE-"}]}],
  "status":{"groups":[{"id":"queued","label":"Queued","color":"#8190a5","statuses":["not_started"]},{"id":"active","label":"Active","color":"#63d4b1","statuses":["in_progress"]},{"id":"terminal","label":"Done","color":"#7aa2ff","statuses":["complete"]}],"active_group_ids":["queued","active"],"terminal_group_ids":["terminal"],"milestone_levels":["milestone","L1"]},
  "view":{"title":"Work recovery","eyebrow":"Internal operations","subtitle":"History and task graph","refresh_seconds":60,"cache_seconds":10,"max_api_response_bytes":33554432,"max_lane_cards":100,"max_graph_nodes":120,"summary_preview_characters":320,"labels":{"inbox":"Recovery inbox","lanes":"KANBAN","tasks":"Tasks","milestones":"Milestones","graph":"Graph","history_search":"History search","related_work":"Related work","copy_resume":"Copy resume packet","copy_task_prompt":"Copy task prompt","sources":"Sources"},"theme":{"background":"#0b0d12","surface":"#131722","surface_alt":"#1a2030","text":"#f4f6fb","muted":"#98a3b7","accent":"#70e1c3","accent_alt":"#7aa2ff","danger":"#ff7a6b","warning":"#e8ad55","success":"#63d4b1"}}
}`
}
