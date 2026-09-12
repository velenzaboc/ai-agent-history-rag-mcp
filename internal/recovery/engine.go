package recovery

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

type Service struct {
	config         Config
	fleet          FleetSource
	history        HistorySource
	now            func() time.Time
	sessionPattern *regexp.Regexp
	cacheMu        sync.RWMutex
	sessionCache   map[string]SessionSummary
}

func NewService(config Config, fleet FleetSource, history HistorySource, now func() time.Time) (*Service, error) {
	if err := config.validate(); err != nil {
		return nil, fmt.Errorf("service config: %w", err)
	}
	if fleet == nil || history == nil {
		return nil, errors.New("fleet and history sources are required")
	}
	if now == nil {
		now = time.Now
	}
	pattern, err := regexp.Compile(config.Matching.SessionIDPattern)
	if err != nil {
		return nil, fmt.Errorf("compile session id pattern: %w", err)
	}
	return &Service{config: config, fleet: fleet, history: history, now: now, sessionPattern: pattern, sessionCache: make(map[string]SessionSummary)}, nil
}

func (service *Service) Dashboard(ctx context.Context) (Dashboard, error) {
	type fleetResult struct {
		snapshot FleetSnapshot
		err      error
	}
	type historyResult struct {
		batch HistoryBatch
		err   error
	}
	fleetChannel := make(chan fleetResult, 1)
	historyChannel := make(chan historyResult, 1)
	statusChannel := make(chan SourceStatus, 1)
	go func() {
		snapshot, err := service.fleet.Snapshot(ctx)
		fleetChannel <- fleetResult{snapshot: snapshot, err: err}
	}()
	go func() {
		batch, err := service.history.Recent(ctx)
		historyChannel <- historyResult{batch: batch, err: err}
	}()
	go func() {
		status, err := service.history.Status(ctx)
		if err != nil && status.Error == "" {
			status.Error = err.Error()
		}
		statusChannel <- status
	}()

	fleetOutcome := <-fleetChannel
	if fleetOutcome.err != nil {
		return Dashboard{}, fmt.Errorf("read fleet snapshot: %w", fleetOutcome.err)
	}
	historyOutcome := <-historyChannel
	historyStatus := <-statusChannel
	now := service.now().UTC()
	fleetStatus := SourceStatus{
		Name:       "fleet",
		Healthy:    true,
		ObservedAt: now,
		Detail: map[string]any{
			"revision":       fleetOutcome.snapshot.Revision,
			"read_timestamp": fleetOutcome.snapshot.ReadTimestamp,
			"store_type":     fleetOutcome.snapshot.StoreType,
		},
	}
	if historyStatus.Name == "" {
		historyStatus.Name = "history"
	}
	if historyStatus.ObservedAt.IsZero() {
		historyStatus.ObservedAt = now
	}
	batch := historyOutcome.batch
	if historyOutcome.err != nil {
		historyStatus.Healthy = false
		historyStatus.Error = historyOutcome.err.Error()
		batch = HistoryBatch{Requested: service.config.History.RecentLimit, Partial: true}
	}
	sessions, probed, missing := service.probeReferencedSessions(ctx, fleetOutcome.snapshot.Worklinks, batch.Sessions)
	service.cacheSessions(sessions)
	relationships := service.match(fleetOutcome.snapshot.Tasks, fleetOutcome.snapshot.Worklinks, sessions)
	findings := service.classify(now, fleetOutcome.snapshot, sessions, relationships, missing)
	milestones := 0
	for _, task := range fleetOutcome.snapshot.Tasks {
		if containsFold(service.config.Status.MilestoneLevels, task.Level) {
			milestones++
		}
	}
	high := 0
	for _, finding := range findings {
		if finding.Severity == "high" {
			high++
		}
	}
	return Dashboard{
		GeneratedAt:   now,
		ProjectID:     fleetOutcome.snapshot.ProjectID,
		Revision:      fleetOutcome.snapshot.Revision,
		ReadTimestamp: fleetOutcome.snapshot.ReadTimestamp,
		Coverage: Coverage{
			FleetLimit: service.config.Fleet.Limit, FleetSnapshotReturned: fleetOutcome.snapshot.SnapshotTasks,
			ActiveTaskLimit: service.config.Fleet.ActiveLimit, ActiveTaskReturned: fleetOutcome.snapshot.ActiveTasks,
			HistoryLimit: service.config.History.RecentLimit, HistoryPartial: batch.Partial,
			HistoryProbed: probed, HistoryReturned: len(sessions),
		},
		View: service.config.View, Status: service.config.Status, Classification: service.config.Classification,
		Counts:  DashboardCounts{Tasks: len(fleetOutcome.snapshot.Tasks), Sessions: len(sessions), Relationships: len(relationships), Findings: len(findings), HighSeverity: high, Milestones: milestones},
		Sources: []SourceStatus{fleetStatus, historyStatus}, Tasks: fleetOutcome.snapshot.Tasks,
		Dependencies: fleetOutcome.snapshot.Dependencies, Worklinks: fleetOutcome.snapshot.Worklinks,
		Sessions: sessions, Relationships: relationships, Findings: findings,
	}, nil
}

func (service *Service) Search(ctx context.Context, search HistorySearch) (HistoryBatch, error) {
	batch, err := service.history.Search(ctx, search)
	if err == nil {
		service.cacheSessions(batch.Sessions)
	}
	return batch, err
}

func (service *Service) Worklinks(ctx context.Context, taskID string) (TaskWorklinks, error) {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" || len(taskID) > 512 {
		return TaskWorklinks{}, errors.New("task_id is required")
	}
	links, err := service.fleet.Worklinks(ctx, taskID)
	if err != nil {
		return TaskWorklinks{}, fmt.Errorf("read task worklinks: %w", err)
	}
	return TaskWorklinks{TaskID: taskID, Worklinks: links}, nil
}

func (service *Service) ResumePacket(ctx context.Context, taskID, sessionID string) (ResumePacket, error) {
	sessionID = strings.TrimSpace(sessionID)
	taskID = strings.TrimSpace(taskID)
	if sessionID == "" {
		return ResumePacket{}, errors.New("session_id is required")
	}
	type fleetResult struct {
		snapshot FleetSnapshot
		err      error
	}
	type historyResult struct {
		session SessionSummary
		err     error
	}
	type worklinksResult struct {
		links []Worklink
		err   error
	}
	fleetChannel := make(chan fleetResult, 1)
	historyChannel := make(chan historyResult, 1)
	worklinksChannel := make(chan worklinksResult, 1)
	go func() {
		snapshot, err := service.fleet.Snapshot(ctx)
		fleetChannel <- fleetResult{snapshot: snapshot, err: err}
	}()
	if cached, ok := service.cachedSession(sessionID); ok {
		historyChannel <- historyResult{session: cached}
	} else {
		go func() {
			session, err := service.history.Session(ctx, sessionID)
			historyChannel <- historyResult{session: session, err: err}
		}()
	}
	if taskID == "" {
		worklinksChannel <- worklinksResult{}
	} else {
		go func() {
			links, err := service.fleet.Worklinks(ctx, taskID)
			worklinksChannel <- worklinksResult{links: links, err: err}
		}()
	}
	historyOutcome := <-historyChannel
	if historyOutcome.err != nil {
		return ResumePacket{}, fmt.Errorf("read session history: %w", historyOutcome.err)
	}
	fleetOutcome := <-fleetChannel
	if fleetOutcome.err != nil {
		return ResumePacket{}, fmt.Errorf("read fleet snapshot: %w", fleetOutcome.err)
	}
	worklinksOutcome := <-worklinksChannel
	if worklinksOutcome.err != nil {
		return ResumePacket{}, fmt.Errorf("read exact task worklinks: %w", worklinksOutcome.err)
	}
	var task *Task
	for index := range fleetOutcome.snapshot.Tasks {
		if fleetOutcome.snapshot.Tasks[index].TaskID == taskID {
			task = &fleetOutcome.snapshot.Tasks[index]
			break
		}
	}
	var builder strings.Builder
	fmt.Fprintf(&builder, "Resume the prior work session %s.\n\n", sessionID)
	builder.WriteString("HISTORY EVIDENCE — use this as the resumption anchor\n")
	fmt.Fprintf(&builder, "Recorded at: %s\nProject: %s\nProject path: %s\nMachine: %s\n\n", formatTime(historyOutcome.session.Timestamp), historyOutcome.session.ProjectName, historyOutcome.session.ProjectPath, historyOutcome.session.MachineID)
	builder.WriteString(strings.TrimSpace(historyOutcome.session.Summary))
	builder.WriteString("\n\n")
	builder.WriteString("LIVE TASK-GRAPH CONTEXT — planning metadata after the history anchor\n")
	if task == nil {
		fmt.Fprintf(&builder, "Task id: %s\nTask record: not present in this bounded snapshot\n", taskID)
	} else {
		fmt.Fprintf(&builder, "Task id: %s\nTitle: %s\nStatus: %s\nOwner: %s\nPillar: %s\nLevel: %s\nUpdated: %s\n", task.TaskID, task.Title, task.Status, task.Owner, task.Pillar, task.Level, formatTime(task.UpdatedAt))
		if task.Note != "" {
			fmt.Fprintf(&builder, "Note: %s\n", task.Note)
		}
	}
	dependencies := dependenciesForTask(fleetOutcome.snapshot.Dependencies, taskID)
	if len(dependencies) > 0 {
		builder.WriteString("Dependencies:\n")
		for _, dependency := range dependencies {
			fmt.Fprintf(&builder, "- %s -> %s (%s)\n", dependency.TaskID, dependency.DependsOnTaskID, dependency.Kind)
		}
	}
	links := worklinksOutcome.links
	if len(links) > 0 {
		builder.WriteString("Durable work links:\n")
		for _, link := range links {
			fmt.Fprintf(&builder, "- %s: %s", link.ArtifactType, link.ArtifactRef)
			if link.Thread != "" {
				fmt.Fprintf(&builder, " (thread %s)", link.Thread)
			}
			builder.WriteByte('\n')
		}
	}
	builder.WriteString("\nContinue from the last concrete action and intent in the history evidence. Verify the referenced branch, worktree, files, and live state before changing anything. Do not substitute the task title or plan seed for the actual resumption point.")
	return ResumePacket{TaskID: taskID, SessionID: sessionID, GeneratedAt: service.now().UTC(), Text: builder.String()}, nil
}

func (service *Service) cacheSessions(sessions []SessionSummary) {
	service.cacheMu.Lock()
	defer service.cacheMu.Unlock()
	for _, session := range sessions {
		if session.SessionID == "" {
			continue
		}
		current, exists := service.sessionCache[session.SessionID]
		if !exists || current.Timestamp.Before(session.Timestamp) || len(current.Summary) < len(session.Summary) {
			service.sessionCache[session.SessionID] = session
		}
	}
	limit := service.config.History.SessionCacheLimit
	if len(service.sessionCache) <= limit {
		return
	}
	ordered := sortedSessions(service.sessionCache)
	keep := make(map[string]SessionSummary, limit)
	for _, session := range ordered[:limit] {
		keep[session.SessionID] = session
	}
	service.sessionCache = keep
}

func (service *Service) cachedSession(sessionID string) (SessionSummary, bool) {
	service.cacheMu.RLock()
	defer service.cacheMu.RUnlock()
	session, ok := service.sessionCache[sessionID]
	return session, ok
}

func (service *Service) probeReferencedSessions(ctx context.Context, links []Worklink, recent []SessionSummary) ([]SessionSummary, int, map[string]bool) {
	byID := make(map[string]SessionSummary)
	for _, session := range recent {
		if session.SessionID == "" {
			continue
		}
		current, exists := byID[session.SessionID]
		if !exists || current.Timestamp.Before(session.Timestamp) || len(current.Summary) < len(session.Summary) {
			byID[session.SessionID] = session
		}
	}
	if service.config.History.ProbeLimit == 0 {
		return sortedSessions(byID), 0, map[string]bool{}
	}
	candidates := make([]string, 0)
	seen := make(map[string]struct{})
	for _, link := range links {
		for _, candidate := range []string{link.Thread, link.ArtifactRef} {
			candidate = strings.TrimSpace(candidate)
			if !service.sessionPattern.MatchString(candidate) {
				continue
			}
			if _, exists := byID[candidate]; exists {
				continue
			}
			if _, exists := seen[candidate]; exists {
				continue
			}
			seen[candidate] = struct{}{}
			candidates = append(candidates, candidate)
			if len(candidates) == service.config.History.ProbeLimit {
				break
			}
		}
		if len(candidates) == service.config.History.ProbeLimit {
			break
		}
	}
	if len(candidates) == 0 {
		return sortedSessions(byID), 0, map[string]bool{}
	}
	type outcome struct {
		id      string
		session SessionSummary
		missing bool
	}
	jobs := make(chan string)
	results := make(chan outcome, len(candidates))
	workers := service.config.History.ProbeConcurrency
	if workers > len(candidates) {
		workers = len(candidates)
	}
	var group sync.WaitGroup
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			for id := range jobs {
				session, err := service.history.Session(ctx, id)
				results <- outcome{id: id, session: session, missing: errors.Is(err, ErrSessionNotFound)}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, id := range candidates {
			select {
			case jobs <- id:
			case <-ctx.Done():
				return
			}
		}
	}()
	group.Wait()
	close(results)
	missing := make(map[string]bool)
	for result := range results {
		if result.session.SessionID != "" {
			byID[result.session.SessionID] = result.session
		}
		if result.missing {
			missing[result.id] = true
		}
	}
	return sortedSessions(byID), len(candidates), missing
}

func sortedSessions(byID map[string]SessionSummary) []SessionSummary {
	sessions := make([]SessionSummary, 0, len(byID))
	for _, session := range byID {
		sessions = append(sessions, session)
	}
	sort.SliceStable(sessions, func(i, j int) bool { return sessions[i].Timestamp.After(sessions[j].Timestamp) })
	return sessions
}

func (service *Service) match(tasks []Task, links []Worklink, sessions []SessionSummary) []Relationship {
	linksByTask := make(map[string][]Worklink)
	for _, link := range links {
		linksByTask[link.TaskID] = append(linksByTask[link.TaskID], link)
	}
	relationships := make([]Relationship, 0)
	for _, session := range sessions {
		var suggestions []Relationship
		for _, task := range tasks {
			score, reasons := service.score(task, linksByTask[task.TaskID], session)
			if score < service.config.Matching.SuggestScore {
				continue
			}
			kind := "suggested"
			if score >= service.config.Matching.AcceptScore {
				kind = "strong"
				if contains(reasons, "exact_session") {
					kind = "exact"
				}
			}
			suggestions = append(suggestions, Relationship{TaskID: task.TaskID, SessionID: session.SessionID, Kind: kind, Score: score, Reasons: reasons})
		}
		sort.SliceStable(suggestions, func(i, j int) bool { return suggestions[i].Score > suggestions[j].Score })
		accepted := 0
		for _, relationship := range suggestions {
			if relationship.Score >= service.config.Matching.AcceptScore {
				relationships = append(relationships, relationship)
				accepted++
			}
		}
		if accepted == 0 && len(suggestions) > 0 {
			relationships = append(relationships, suggestions[0])
		}
	}
	return relationships
}

func (service *Service) score(task Task, links []Worklink, session SessionSummary) (int, []string) {
	score := 0
	reasons := make([]string, 0, 4)
	exactSession := false
	exactPath := false
	projectName := false
	for _, link := range links {
		if session.SessionID != "" && (strings.EqualFold(link.Thread, session.SessionID) || strings.EqualFold(link.ArtifactRef, session.SessionID)) {
			exactSession = true
		}
		ref := service.normalizedPath(link.ArtifactRef)
		projectPath := service.normalizedPath(session.ProjectPath)
		if ref != "" && projectPath != "" && ref == projectPath {
			exactPath = true
		}
		if session.ProjectName != "" && ref != "" && strings.EqualFold(filepath.Base(ref), session.ProjectName) {
			projectName = true
		}
	}
	if exactSession {
		score += service.config.Matching.Weights["exact_session"]
		reasons = append(reasons, "exact_session")
	}
	if exactPath {
		score += service.config.Matching.Weights["exact_path"]
		reasons = append(reasons, "exact_path")
	}
	if projectName {
		score += service.config.Matching.Weights["project_name"]
		reasons = append(reasons, "project_name")
	}
	if tokenOverlap(task.TaskID+" "+task.Title+" "+task.Pillar, session.ProjectName) {
		score += service.config.Matching.Weights["title_token"]
		reasons = append(reasons, "title_token")
	}
	return score, reasons
}

func (service *Service) normalizedPath(value string) string {
	normalized := strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	for _, rewrite := range service.config.Matching.PathRewrites {
		from := strings.ReplaceAll(rewrite.From, "\\", "/")
		to := strings.ReplaceAll(rewrite.To, "\\", "/")
		if strings.HasPrefix(strings.ToLower(normalized), strings.ToLower(from)) {
			normalized = to + normalized[len(from):]
		}
	}
	return strings.ToLower(strings.TrimRight(normalized, "/"))
}

func (service *Service) classify(now time.Time, snapshot FleetSnapshot, sessions []SessionSummary, relationships []Relationship, missing map[string]bool) []Finding {
	rules := make(map[string]ClassificationRule)
	for _, rule := range service.config.Classification.Rules {
		rules[rule.ID] = rule
	}
	taskByID := make(map[string]Task)
	for _, task := range snapshot.Tasks {
		taskByID[task.TaskID] = task
	}
	acceptedBySession := make(map[string][]Relationship)
	acceptedByTask := make(map[string][]Relationship)
	for _, relationship := range relationships {
		if relationship.Kind == "suggested" {
			continue
		}
		acceptedBySession[relationship.SessionID] = append(acceptedBySession[relationship.SessionID], relationship)
		acceptedByTask[relationship.TaskID] = append(acceptedByTask[relationship.TaskID], relationship)
	}
	findings := make([]Finding, 0)
	for _, session := range sessions {
		age := ageHours(now, session.Timestamp)
		accepted := acceptedBySession[session.SessionID]
		if len(accepted) == 0 {
			kind := "orphan_session"
			detail := "No accepted task relationship was derived from configured exact or strong evidence."
			if age >= service.config.Classification.AbandonedAfterHours {
				kind = "abandoned_session"
				detail = "An old session has no accepted task relationship and is a recovery candidate."
			}
			findings = append(findings, configuredFinding(rules[kind], kind, "", session.SessionID, age, detail, session.Timestamp))
		}
		if len(accepted) > 1 {
			findings = append(findings, configuredFinding(rules["ambiguous_binding"], "ambiguous_binding", "", session.SessionID, age, "More than one task meets the configured acceptance score.", session.Timestamp))
		}
		for _, relationship := range accepted {
			task := taskByID[relationship.TaskID]
			if service.isActive(task.Status) && age >= service.config.Classification.StaleAfterHours {
				findings = append(findings, configuredFinding(rules["stale_thread"], "stale_thread", task.TaskID, session.SessionID, age, "A non-terminal task is linked to a session older than the configured stale threshold.", session.Timestamp))
			}
		}
	}
	linksByTask := make(map[string][]Worklink)
	for _, link := range snapshot.Worklinks {
		linksByTask[link.TaskID] = append(linksByTask[link.TaskID], link)
		for _, candidate := range []string{link.Thread, link.ArtifactRef} {
			if missing[candidate] {
				task := taskByID[link.TaskID]
				if service.isActive(task.Status) {
					findings = append(findings, configuredFinding(rules["missing_history"], "missing_history", link.TaskID, candidate, 0, "A configured session-shaped work link was probed and returned no history summary.", task.UpdatedAt))
				}
			}
		}
	}
	for _, task := range snapshot.Tasks {
		if task.Projection != "active_overlay" && service.isActive(task.Status) && len(linksByTask[task.TaskID]) == 0 && len(acceptedByTask[task.TaskID]) == 0 {
			findings = append(findings, configuredFinding(rules["unlinked_task"], "unlinked_task", task.TaskID, "", ageHours(now, task.UpdatedAt), "A non-terminal task has no durable work link in the bounded fleet snapshot.", task.UpdatedAt))
		}
	}
	severityOrder := map[string]int{"high": 0, "medium": 1, "low": 2}
	sort.SliceStable(findings, func(i, j int) bool {
		if severityOrder[findings[i].Severity] != severityOrder[findings[j].Severity] {
			return severityOrder[findings[i].Severity] < severityOrder[findings[j].Severity]
		}
		return findings[i].SortTime.Before(findings[j].SortTime)
	})
	return findings
}

func (service *Service) isActive(status string) bool {
	group := ""
	for _, candidate := range service.config.Status.Groups {
		if containsFold(candidate.Statuses, status) {
			group = candidate.ID
			break
		}
	}
	return contains(service.config.Status.ActiveGroupIDs, group)
}

func configuredFinding(rule ClassificationRule, kind, taskID, sessionID string, age int, detail string, sortTime time.Time) Finding {
	return Finding{Kind: kind, Label: rule.Label, Severity: rule.Severity, Color: rule.Color, TaskID: taskID, SessionID: sessionID, AgeHours: age, Detail: detail, SortTime: sortTime}
}

func ageHours(now, then time.Time) int {
	if then.IsZero() || then.After(now) {
		return 0
	}
	return int(now.Sub(then).Hours())
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func containsFold(values []string, target string) bool {
	for _, value := range values {
		if strings.EqualFold(value, target) {
			return true
		}
	}
	return false
}

func tokenOverlap(task, project string) bool {
	projectTokens := tokenize(project)
	if len(projectTokens) == 0 {
		return false
	}
	taskTokens := make(map[string]struct{})
	for _, token := range tokenize(task) {
		taskTokens[token] = struct{}{}
	}
	for _, token := range projectTokens {
		if len(token) >= 3 {
			if _, exists := taskTokens[token]; exists {
				return true
			}
		}
	}
	return false
}

func tokenize(value string) []string {
	return strings.FieldsFunc(strings.ToLower(value), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9')
	})
}

func dependenciesForTask(dependencies []Dependency, taskID string) []Dependency {
	result := make([]Dependency, 0)
	for _, dependency := range dependencies {
		if dependency.TaskID == taskID || dependency.DependsOnTaskID == taskID {
			result = append(result, dependency)
		}
	}
	return result
}

func worklinksForTask(links []Worklink, taskID string) []Worklink {
	result := make([]Worklink, 0)
	for _, link := range links {
		if link.TaskID == taskID {
			result = append(result, link)
		}
	}
	return result
}

func formatTime(value time.Time) string {
	if value.IsZero() {
		return "unknown"
	}
	return value.UTC().Format(time.RFC3339)
}
