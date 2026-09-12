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
	for _, expected := range []string{"Execute fleet-plan task T-1", "Implement task prompt", "Acceptance requires a copyable prompt.", "T-2", "T-3", "/work/task-prompt", "r-task"} {
		if !strings.Contains(prompt.Text, expected) {
			t.Fatalf("task prompt missing %q:\n%s", expected, prompt.Text)
		}
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
	snapshot  FleetSnapshot
	scoped    FleetSnapshot
	worklinks []Worklink
	err       error
}

func (s stubFleet) Snapshot(context.Context) (FleetSnapshot, error) { return s.snapshot, s.err }
func (s stubFleet) ScopedSnapshot(context.Context, string, string, int) (FleetSnapshot, error) {
	return s.scoped, s.err
}
func (s stubFleet) Worklinks(context.Context, string) ([]Worklink, error) {
	return s.worklinks, s.err
}

type stubHistory struct {
	recent       HistoryBatch
	session      SessionSummary
	search       HistoryBatch
	err          error
	sessionErr   error
	sessionCalls *int
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
func (s stubHistory) Search(context.Context, HistorySearch) (HistoryBatch, error) {
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
