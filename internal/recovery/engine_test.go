package recovery

import (
	"context"
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
	if !hasFinding(dashboard.Findings, "orphan_session", "", "00000000-0000-0000-0000-000000000002") {
		t.Fatalf("old orphan classification missing: %#v", dashboard.Findings)
	}
	if !hasFinding(dashboard.Findings, "unlinked_task", "T-2", "") {
		t.Fatalf("unlinked task classification missing: %#v", dashboard.Findings)
	}
}

func TestResumePacketOrdersHistoryBeforeGraphContext(t *testing.T) {
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	cfg := mustTestConfig(t)
	fleet := stubFleet{snapshot: FleetSnapshot{ProjectID: "project-a", Tasks: []Task{{TaskID: "T-1", Title: "Task one", Status: "in_progress", UpdatedAt: now}}}}
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
	if historyAt < 0 || graphAt < 0 || historyAt >= graphAt {
		t.Fatalf("resume packet did not preserve evidence order:\n%s", packet.Text)
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
	snapshot FleetSnapshot
	err      error
}

func (s stubFleet) Snapshot(context.Context) (FleetSnapshot, error) { return s.snapshot, s.err }

type stubHistory struct {
	recent  HistoryBatch
	session SessionSummary
	search  HistoryBatch
	err     error
}

func (s stubHistory) Recent(context.Context) (HistoryBatch, error) { return s.recent, s.err }
func (s stubHistory) Session(context.Context, string) (SessionSummary, error) {
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
