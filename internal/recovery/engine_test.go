package recovery

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestBuildDashboardLinksExactSessionsAndClassifiesOldOrphans(t *testing.T) {
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	cfg := mustTestConfig(t)
	fleet := stubFleet{snapshot: FleetSnapshot{
		ProjectID: "project-a",
		Tasks: []Task{
			{TaskID: "T-1", Title: "Implement recovery", Status: "in_progress", Level: "task", UpdatedAt: now.Add(-2 * time.Hour)},
			{TaskID: "T-2", Title: "Unclaimed work", Status: "not_started", Level: "task", UpdatedAt: now.Add(-48 * time.Hour)},
		},
		Worklinks: []Worklink{{TaskID: "T-1", ArtifactType: "thread", ArtifactRef: "00000000-0000-0000-0000-000000000001", Thread: "00000000-0000-0000-0000-000000000001"}},
	}}
	history := stubHistory{recent: HistoryBatch{Sessions: []SessionSummary{
		{SessionID: "00000000-0000-0000-0000-000000000001", Summary: "exact", Timestamp: now.Add(-2 * time.Hour)},
		{SessionID: "00000000-0000-0000-0000-000000000002", Summary: "old orphan", Timestamp: now.Add(-1000 * time.Hour)},
	}}}
	service, err := NewService(cfg, fleet, history, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	dashboard, err := service.Dashboard(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(dashboard.Relationships) != 1 || dashboard.Relationships[0].Kind != "exact" {
		t.Fatalf("unexpected relationships: %#v", dashboard.Relationships)
	}
	if !hasFinding(dashboard.Findings, "abandoned_session", "", "00000000-0000-0000-0000-000000000002") {
		t.Fatalf("old orphan classification missing: %#v", dashboard.Findings)
	}
	if !hasFinding(dashboard.Findings, "unlinked_task", "T-2", "") {
		t.Fatalf("unlinked task classification missing: %#v", dashboard.Findings)
	}
}

func TestBuildDashboardDoesNotInferMissingWorklinkForOverlayOnlyTask(t *testing.T) {
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	cfg := mustTestConfig(t)
	fleet := stubFleet{snapshot: FleetSnapshot{ProjectID: "project-a", Tasks: []Task{{TaskID: "T-1", Title: "Current task", Status: "in_progress", Projection: "active_overlay", UpdatedAt: now}}}}
	service, err := NewService(cfg, fleet, stubHistory{}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	dashboard, err := service.Dashboard(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if hasFinding(dashboard.Findings, "unlinked_task", "T-1", "") {
		t.Fatalf("overlay-only task was treated as proof of a missing worklink: %#v", dashboard.Findings)
	}
}

func TestResumePacketOrdersHistoryBeforeGraphContext(t *testing.T) {
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	cfg := mustTestConfig(t)
	fleet := stubFleet{snapshot: FleetSnapshot{ProjectID: "project-a", Tasks: []Task{{TaskID: "T-1", Title: "Task one", Status: "in_progress", UpdatedAt: now}}}, worklinks: []Worklink{{TaskID: "T-1", ArtifactType: "pr", ArtifactRef: "https://example.invalid/pr/1"}}}
	history := stubHistory{session: SessionSummary{SessionID: "s-1", Summary: "LAST ACTION: ran the validator. INTENT: correct the rejected row.", Timestamp: now}}
	service, err := NewService(cfg, fleet, history, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	packet, err := service.ResumePacket(context.Background(), "T-1", "s-1")
	if err != nil {
		t.Fatal(err)
	}
	historyAt := strings.Index(packet.Text, "LAST ACTION")
	graphAt := strings.Index(packet.Text, "Task one")
	if historyAt < 0 || graphAt < 0 || historyAt >= graphAt || !strings.Contains(packet.Text, "https://example.invalid/pr/1") {
		t.Fatalf("resume packet did not preserve evidence order:\n%s", packet.Text)
	}
}

func TestResumePacketUsesDashboardSessionCache(t *testing.T) {
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	cfg := mustTestConfig(t)
	fleet := stubFleet{snapshot: FleetSnapshot{ProjectID: "project-a", Tasks: []Task{{TaskID: "T-1", Title: "Task one", Status: "in_progress", UpdatedAt: now}}}}
	calls := 0
	history := stubHistory{
		recent:       HistoryBatch{Sessions: []SessionSummary{{SessionID: "s-1", Summary: "cached history anchor", Timestamp: now}}},
		sessionErr:   errors.New("exact session endpoint unavailable"),
		sessionCalls: &calls,
	}
	service, err := NewService(cfg, fleet, history, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Dashboard(context.Background()); err != nil {
		t.Fatal(err)
	}
	packet, err := service.ResumePacket(context.Background(), "T-1", "s-1")
	if err != nil {
		t.Fatal(err)
	}
	if calls != 0 || !strings.Contains(packet.Text, "cached history anchor") {
		t.Fatalf("resume packet bypassed the session cache: calls=%d packet=%q", calls, packet.Text)
	}
}

func TestTaskPromptUsesScopedTaskAuthorityWithoutSession(t *testing.T) {
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	cfg := mustTestConfig(t)
	fleet := stubFleet{
		scoped: FleetSnapshot{
			ProjectID: "project-a", Revision: "r-task", ReadTimestamp: "2026-09-11T12:00:00Z",
			Tasks: []Task{
				{TaskID: "T-1", Title: "Implement task prompt", Status: "in_progress", Owner: "thread-a", Pillar: "delivery", Level: "task", Note: "Acceptance requires a copyable prompt.", UpdatedAt: now},
				{TaskID: "T-2", ParentID: "T-1", Title: "Validate task prompt", Status: "not_started", Owner: "thread-b"},
			},
			Dependencies: []Dependency{{TaskID: "T-1", DependsOnTaskID: "T-3", Kind: "gated_by"}},
		},
		worklinks: []Worklink{{TaskID: "T-1", ArtifactType: "worktree", ArtifactRef: "/work/task-prompt", Thread: "thread-a"}},
	}
	service, err := NewService(cfg, fleet, stubHistory{}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	prompt, err := service.TaskPrompt(context.Background(), "T-1")
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"Execute fleet-plan task T-1", "Implement task prompt", "Acceptance requires a copyable prompt.", "T-2", "T-3", "/work/task-prompt", "r-task", "RELATED-WORK PREFLIGHT"} {
		if !strings.Contains(prompt.Text, expected) {
			t.Fatalf("task prompt missing %q:\n%s", expected, prompt.Text)
		}
	}
}

func TestRelatedWorkRunsAllConfiguredPassesAndClassifiesCandidates(t *testing.T) {
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	cfg := mustTestConfig(t)
	searches := make(chan HistorySearch, 2)
	fleet := stubFleet{
		scoped: FleetSnapshot{
			ProjectID: "project-a", Revision: "r-related", ReadTimestamp: "2026-09-11T12:00:00Z",
			Tasks: []Task{
				{TaskID: "T-1", ParentID: "ROOT", Title: "Build recovery console prompt", Status: "in_progress", Pillar: "delivery", Note: "Find and reuse existing agent work."},
				{TaskID: "T-CONTEXT", ParentID: "T-1", Title: "Validate recovery console", Status: "not_started", Pillar: "delivery"},
			},
		},
		search: []Task{
			{TaskID: "T-1", Title: "Build recovery console prompt", Status: "in_progress", Pillar: "delivery"},
			{TaskID: "T-OLD", ParentID: "ROOT", Title: "Prior recovery console prompt", Status: "complete", Pillar: "delivery"},
			{TaskID: "T-LIVE", ParentID: "ROOT", Title: "Recovery console implementation", Status: "in_progress", Pillar: "delivery"},
			{TaskID: "T-NOISE", Title: "Unrelated work", Status: "in_progress", Note: "Find and reuse existing agent work."},
		},
		worklinksByTask: map[string][]Worklink{
			"T-1":    {{TaskID: "T-1", ArtifactType: "worktree", ArtifactRef: "/work/recovery"}},
			"T-OLD":  {{TaskID: "T-OLD", ArtifactType: "commit", ArtifactRef: "abc123"}},
			"T-LIVE": {{TaskID: "T-LIVE", ArtifactType: "worktree", ArtifactRef: "/work/recovery"}},
		},
	}
	history := stubHistory{
		search:   HistoryBatch{Sessions: []SessionSummary{{SessionID: "s-related", ChunkID: "chunk-related", Summary: "Implemented a recovery console prompt and validator", Timestamp: now.Add(-time.Hour)}}},
		searches: searches,
	}
	service, err := NewService(cfg, fleet, history, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	packet, err := service.RelatedWork(context.Background(), "T-1")
	if err != nil {
		t.Fatal(err)
	}
	if packet.Query == "" || packet.Decision.ID != "collision" || !packet.Complete || len(packet.Passes) != 5 {
		t.Fatalf("unexpected related-work packet: %#v", packet)
	}
	if len(searches) != 2 || len(packet.History) != 1 {
		t.Fatalf("history passes were not bounded and deduplicated: searches=%#v history=%#v", searches, packet.History)
	}
	verdicts := map[string]string{}
	for _, candidate := range packet.Candidates {
		verdicts[candidate.Task.TaskID] = candidate.Verdict.ID
	}
	if verdicts["T-LIVE"] != "collision" || verdicts["T-OLD"] != "reuse" {
		t.Fatalf("unexpected candidate verdicts: %#v", verdicts)
	}
	if _, noisy := verdicts["T-NOISE"]; noisy {
		t.Fatalf("note-only generic overlap crossed the related-work threshold: %#v", verdicts)
	}
}

func TestProgramViewSelectsConfiguredLaneRoots(t *testing.T) {
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	cfg := mustTestConfig(t)
	fleet := stubFleet{scoped: FleetSnapshot{
		ProjectID: "project-a", Revision: "r-program", ReadTimestamp: "2026-09-11T12:00:00Z",
		Tasks: []Task{
			{TaskID: "ROOT-1", Title: "Program root", Status: "in_progress"},
			{TaskID: "LANE-A", ParentID: "ROOT-1", Title: "Lane A", Status: "in_progress"},
			{TaskID: "LANE-B", ParentID: "ROOT-1", Title: "Lane B", Status: "not_started"},
			{TaskID: "CONTROL", ParentID: "ROOT-1", Title: "Control", Status: "done"},
			{TaskID: "PLAN-A", ParentID: "LANE-A", Title: "Plan A", Status: "done"},
		},
	}}
	service, err := NewService(cfg, fleet, stubHistory{}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	program, err := service.Program(context.Background(), "delivery")
	if err != nil {
		t.Fatal(err)
	}
	if program.Counts.Lanes != 2 || !program.Counts.LaneCountMatches || len(program.LaneTaskIDs) != 2 || program.Counts.Tasks != 5 {
		t.Fatalf("unexpected program view: %#v", program)
	}
}

func hasFinding(findings []Finding, kind, taskID, sessionID string) bool {
	for _, finding := range findings {
		if finding.Kind == kind && finding.TaskID == taskID && finding.SessionID == sessionID {
			return true
		}
	}
	return false
}

type stubFleet struct {
	snapshot        FleetSnapshot
	scoped          FleetSnapshot
	worklinks       []Worklink
	worklinksByTask map[string][]Worklink
	search          []Task
	err             error
}

func (s stubFleet) Snapshot(context.Context) (FleetSnapshot, error) { return s.snapshot, s.err }
func (s stubFleet) ScopedSnapshot(context.Context, string, string, int) (FleetSnapshot, error) {
	return s.scoped, s.err
}

func (s stubFleet) Worklinks(_ context.Context, taskID string) ([]Worklink, error) {
	if s.worklinksByTask != nil {
		return s.worklinksByTask[taskID], s.err
	}
	return s.worklinks, s.err
}
func (s stubFleet) Search(context.Context, string, int) ([]Task, error) { return s.search, s.err }

type stubHistory struct {
	recent       HistoryBatch
	session      SessionSummary
	search       HistoryBatch
	err          error
	sessionErr   error
	sessionCalls *int
	searches     chan<- HistorySearch
}

func (s stubHistory) Recent(context.Context) (HistoryBatch, error) { return s.recent, s.err }
func (s stubHistory) Session(context.Context, string) (SessionSummary, error) {
	if s.sessionCalls != nil {
		*s.sessionCalls++
	}
	if s.sessionErr != nil {
		return SessionSummary{}, s.sessionErr
	}
	return s.session, s.err
}
func (s stubHistory) Search(_ context.Context, search HistorySearch) (HistoryBatch, error) {
	if s.searches != nil {
		s.searches <- search
	}
	return s.search, s.err
}
func (s stubHistory) Status(context.Context) (SourceStatus, error) {
	return SourceStatus{Name: "history", Healthy: s.err == nil}, s.err
}

func mustTestConfig(t *testing.T) Config {
	t.Helper()
	cfg, err := LoadConfig(writeConfig(t, validConfigJSON()))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}
