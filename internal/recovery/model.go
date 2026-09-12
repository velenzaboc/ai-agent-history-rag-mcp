package recovery

import (
	"encoding/json"
	"time"
)

type FleetSnapshot struct {
	Collections   map[string]any  `json:"collections,omitempty"`
	ContentHashes map[string]any  `json:"content_hashes,omitempty"`
	Counts        map[string]any  `json:"counts,omitempty"`
	Dependencies  []Dependency    `json:"dependencies"`
	GeneratedAt   string          `json:"generated_at,omitempty"`
	Limit         int             `json:"limit,omitempty"`
	ProjectID     string          `json:"project_id"`
	ReadTimestamp string          `json:"read_timestamp,omitempty"`
	Revision      string          `json:"revision,omitempty"`
	Scope         json.RawMessage `json:"scope,omitempty"`
	StoreType     string          `json:"store_type,omitempty"`
	Tasks         []Task          `json:"tasks"`
	Worklinks     []Worklink      `json:"worklinks"`
}

type Task struct {
	IsCriticalPath bool      `json:"is_critical_path"`
	IsGap          bool      `json:"is_gap"`
	Level          string    `json:"level"`
	Note           string    `json:"note"`
	Owner          string    `json:"owner"`
	Pillar         string    `json:"pillar"`
	ProjectID      string    `json:"project_id"`
	Status         string    `json:"status"`
	TaskID         string    `json:"task_id"`
	Title          string    `json:"title"`
	UpdatedAt      time.Time `json:"updated_at"`
}

func (t *Task) UnmarshalJSON(payload []byte) error {
	type taskAlias Task
	var raw struct {
		taskAlias
		UpdatedAt string `json:"updated_at"`
	}
	if err := unmarshalJSON(payload, &raw); err != nil {
		return err
	}
	*t = Task(raw.taskAlias)
	t.UpdatedAt = parseTimestamp(raw.UpdatedAt)
	return nil
}

type Dependency struct {
	CreatedAt       string `json:"created_at,omitempty"`
	DependencyID    string `json:"dependency_id"`
	DependsOnTaskID string `json:"depends_on_task_id"`
	Kind            string `json:"kind"`
	ProjectID       string `json:"project_id"`
	TaskID          string `json:"task_id"`
}

type Worklink struct {
	ArtifactID   string `json:"artifact_id"`
	ArtifactRef  string `json:"artifact_ref"`
	ArtifactType string `json:"artifact_type"`
	CreatedAt    string `json:"created_at,omitempty"`
	Note         string `json:"note"`
	ProjectID    string `json:"project_id"`
	TaskID       string `json:"task_id"`
	Thread       string `json:"thread"`
}

type SessionSummary struct {
	ChunkID     string    `json:"chunk_id"`
	SessionID   string    `json:"session_id"`
	Summary     string    `json:"summary"`
	ChunkType   string    `json:"chunk_type"`
	ProjectPath string    `json:"project_path"`
	ProjectName string    `json:"project_name"`
	Timestamp   time.Time `json:"timestamp"`
	FilePath    string    `json:"file_path,omitempty"`
	Operation   string    `json:"operation,omitempty"`
	MachineID   string    `json:"machine_id,omitempty"`
	Score       float64   `json:"score,omitempty"`
}

type HistoryBatch struct {
	Sessions  []SessionSummary `json:"sessions"`
	Requested int              `json:"requested"`
	Returned  int              `json:"returned"`
	Partial   bool             `json:"partial"`
}

type HistorySearch struct {
	Query         string `json:"query"`
	ProjectFilter string `json:"project_filter,omitempty"`
	DateFrom      string `json:"date_from,omitempty"`
	DateTo        string `json:"date_to,omitempty"`
	Limit         int    `json:"limit"`
	FilePath      string `json:"file_path,omitempty"`
	Operation     string `json:"operation,omitempty"`
	Files         bool   `json:"files,omitempty"`
}

type SourceStatus struct {
	Name       string         `json:"name"`
	Healthy    bool           `json:"healthy"`
	ObservedAt time.Time      `json:"observed_at"`
	Detail     map[string]any `json:"detail,omitempty"`
	Error      string         `json:"error,omitempty"`
}

type Relationship struct {
	TaskID    string   `json:"task_id"`
	SessionID string   `json:"session_id"`
	Kind      string   `json:"kind"`
	Score     int      `json:"score"`
	Reasons   []string `json:"reasons"`
}

type Finding struct {
	Kind      string    `json:"kind"`
	Label     string    `json:"label"`
	Severity  string    `json:"severity"`
	Color     string    `json:"color"`
	TaskID    string    `json:"task_id,omitempty"`
	SessionID string    `json:"session_id,omitempty"`
	AgeHours  int       `json:"age_hours,omitempty"`
	Detail    string    `json:"detail"`
	SortTime  time.Time `json:"-"`
}

type DashboardCounts struct {
	Tasks         int `json:"tasks"`
	Sessions      int `json:"sessions"`
	Relationships int `json:"relationships"`
	Findings      int `json:"findings"`
	HighSeverity  int `json:"high_severity"`
	Milestones    int `json:"milestones"`
}

type Dashboard struct {
	GeneratedAt    time.Time            `json:"generated_at"`
	ProjectID      string               `json:"project_id"`
	Revision       string               `json:"revision"`
	ReadTimestamp  string               `json:"read_timestamp"`
	Coverage       Coverage             `json:"coverage"`
	View           ViewConfig           `json:"view"`
	Status         StatusConfig         `json:"status"`
	Classification ClassificationConfig `json:"classification"`
	Counts         DashboardCounts      `json:"counts"`
	Sources        []SourceStatus       `json:"sources"`
	Tasks          []Task               `json:"tasks"`
	Dependencies   []Dependency         `json:"dependencies"`
	Worklinks      []Worklink           `json:"worklinks"`
	Sessions       []SessionSummary     `json:"sessions"`
	Relationships  []Relationship       `json:"relationships"`
	Findings       []Finding            `json:"findings"`
}

type Coverage struct {
	FleetLimit      int  `json:"fleet_limit"`
	HistoryLimit    int  `json:"history_limit"`
	HistoryPartial  bool `json:"history_partial"`
	HistoryProbed   int  `json:"history_probed"`
	HistoryReturned int  `json:"history_returned"`
}

type ResumePacket struct {
	TaskID      string    `json:"task_id"`
	SessionID   string    `json:"session_id"`
	GeneratedAt time.Time `json:"generated_at"`
	Text        string    `json:"text"`
}
