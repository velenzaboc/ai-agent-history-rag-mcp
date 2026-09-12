package recovery

import (
	"context"
	"encoding/json"
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
		View: service.config.View, Status: service.config.Status, Classification: service.config.Classification, Programs: service.config.Programs,
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

func (service *Service) Program(ctx context.Context, programID string) (ProgramView, error) {
	programID = strings.TrimSpace(programID)
	var program *ProgramConfig
	for index := range service.config.Programs {
		if service.config.Programs[index].ID == programID {
			program = &service.config.Programs[index]
			break
		}
	}
	if program == nil {
		return ProgramView{}, errors.New("program is not configured")
	}
	snapshot, err := service.fleet.ScopedSnapshot(ctx, "execution_subtree", program.RootTaskID, program.Limit)
	if err != nil {
		return ProgramView{}, fmt.Errorf("read program snapshot: %w", err)
	}
	rootPresent := false
	laneTaskIDs := make([]string, 0, program.ExpectedLaneCount)
	byStatus := make(map[string]int)
	for _, task := range snapshot.Tasks {
		if task.TaskID == program.RootTaskID {
			rootPresent = true
		}
		byStatus[task.Status]++
		if task.ParentID == program.RootTaskID && strings.HasPrefix(task.TaskID, program.LaneTaskIDPrefix) {
			laneTaskIDs = append(laneTaskIDs, task.TaskID)
		}
	}
	if !rootPresent {
		return ProgramView{}, errors.New("program root is not present in its scoped snapshot")
	}
	sort.Strings(laneTaskIDs)
	return ProgramView{
		Program: *program, GeneratedAt: service.now().UTC(), ProjectID: snapshot.ProjectID,
		Revision: snapshot.Revision, ReadTimestamp: snapshot.ReadTimestamp, StoreType: snapshot.StoreType,
		Scope: snapshot.Scope, Collections: snapshot.Collections,
		Counts: ProgramCounts{
			Tasks: len(snapshot.Tasks), Lanes: len(laneTaskIDs), ExpectedLanes: program.ExpectedLaneCount,
			LaneCountMatches: len(laneTaskIDs) == program.ExpectedLaneCount,
			Dependencies:     len(snapshot.Dependencies), Worklinks: len(snapshot.Worklinks), ByStatus: byStatus,
		},
		LaneTaskIDs: laneTaskIDs, Tasks: snapshot.Tasks, Dependencies: snapshot.Dependencies, Worklinks: snapshot.Worklinks,
	}, nil
}

func (service *Service) RelatedWork(ctx context.Context, taskID string) (RelatedWorkPacket, error) {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" || len(taskID) > 512 {
		return RelatedWorkPacket{}, errors.New("task_id is required")
	}
	type snapshotResult struct {
		snapshot FleetSnapshot
		err      error
	}
	type worklinksResult struct {
		links []Worklink
		err   error
	}
	snapshotChannel := make(chan snapshotResult, 1)
	worklinksChannel := make(chan worklinksResult, 1)
	go func() {
		snapshot, err := service.fleet.ScopedSnapshot(ctx, "acceptance_dependency_closure", taskID, service.config.Fleet.TaskPromptLimit)
		snapshotChannel <- snapshotResult{snapshot: snapshot, err: err}
	}()
	go func() {
		links, err := service.fleet.Worklinks(ctx, taskID)
		worklinksChannel <- worklinksResult{links: links, err: err}
	}()
	snapshotOutcome := <-snapshotChannel
	if snapshotOutcome.err != nil {
		return RelatedWorkPacket{}, fmt.Errorf("read task context: %w", snapshotOutcome.err)
	}
	worklinksOutcome := <-worklinksChannel
	if worklinksOutcome.err != nil {
		return RelatedWorkPacket{}, fmt.Errorf("read exact task worklinks: %w", worklinksOutcome.err)
	}
	var task *Task
	for index := range snapshotOutcome.snapshot.Tasks {
		if snapshotOutcome.snapshot.Tasks[index].TaskID == taskID {
			task = &snapshotOutcome.snapshot.Tasks[index]
			break
		}
	}
	if task == nil {
		return RelatedWorkPacket{}, errors.New("task is not present in its scoped snapshot")
	}
	query := service.discoveryQuery(*task)
	if query == "" {
		return RelatedWorkPacket{}, errors.New("configured task fields produced no discovery query")
	}

	type taskSearchResult struct {
		tasks []Task
		err   error
	}
	type historySearchResult struct {
		kind  string
		batch HistoryBatch
		err   error
	}
	taskSearchChannel := make(chan taskSearchResult, 1)
	historySearchCount := 0
	if service.config.Discovery.SearchConversations {
		historySearchCount++
	}
	if service.config.Discovery.SearchFiles {
		historySearchCount++
	}
	historySearchChannel := make(chan historySearchResult, historySearchCount)
	go func() {
		tasks, err := service.fleet.Search(ctx, query, service.config.Discovery.CandidateLimit)
		taskSearchChannel <- taskSearchResult{tasks: tasks, err: err}
	}()
	if service.config.Discovery.SearchConversations {
		go func() {
			batch, err := service.history.Search(ctx, HistorySearch{Query: query, Limit: service.config.Discovery.HistoryLimit})
			historySearchChannel <- historySearchResult{kind: "conversations", batch: batch, err: err}
		}()
	}
	if service.config.Discovery.SearchFiles {
		go func() {
			batch, err := service.history.Search(ctx, HistorySearch{Query: query, Limit: service.config.Discovery.HistoryLimit, Files: true})
			historySearchChannel <- historySearchResult{kind: "files", batch: batch, err: err}
		}()
	}

	taskSearchOutcome := <-taskSearchChannel
	searchCandidates := make([]Task, 0, len(taskSearchOutcome.tasks))
	seenTasks := make(map[string]struct{}, len(taskSearchOutcome.tasks))
	for _, candidate := range taskSearchOutcome.tasks {
		if candidate.TaskID == taskID {
			mergeTaskProjection(task, candidate)
			continue
		}
		if candidate.TaskID == "" {
			continue
		}
		if _, exists := seenTasks[candidate.TaskID]; exists {
			continue
		}
		seenTasks[candidate.TaskID] = struct{}{}
		searchCandidates = append(searchCandidates, candidate)
	}

	historyComplete := true
	historyErrors := make([]string, 0)
	historyByKey := make(map[string]SessionSummary)
	for range historySearchCount {
		outcome := <-historySearchChannel
		if outcome.err != nil {
			historyComplete = false
			historyErrors = append(historyErrors, outcome.kind+": "+outcome.err.Error())
			continue
		}
		for _, session := range outcome.batch.Sessions {
			key := historyEvidenceKey(session)
			current, exists := historyByKey[key]
			if !exists || current.Timestamp.Before(session.Timestamp) || len(current.Summary) < len(session.Summary) {
				historyByKey[key] = session
			}
		}
	}
	history := sortedSessions(historyByKey)
	if len(history) > service.config.Discovery.HistoryLimit {
		history = history[:service.config.Discovery.HistoryLimit]
	}
	service.cacheSessions(history)

	hydrated, hydrationErrors := service.hydrateCandidateWorklinks(ctx, searchCandidates)
	candidates := make([]RelatedTaskCandidate, 0, len(searchCandidates))
	for _, candidate := range searchCandidates {
		links := hydrated[candidate.TaskID]
		score, reasons, sharedArtifact := service.scoreRelatedTask(*task, worklinksOutcome.links, candidate, links)
		verdictID := ""
		if sharedArtifact && service.isActive(candidate.Status) {
			verdictID = "collision"
		} else if service.isTerminal(candidate.Status) && score >= service.config.Discovery.Thresholds.ReuseScore {
			verdictID = "reuse"
		} else if score >= service.config.Discovery.Thresholds.RelatedScore {
			verdictID = "related"
		}
		if verdictID == "" {
			continue
		}
		candidates = append(candidates, RelatedTaskCandidate{Task: candidate, Score: score, Verdict: service.discoveryVerdict(verdictID, ""), Reasons: reasons, Worklinks: links})
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Verdict.ID != candidates[j].Verdict.ID {
			priority := map[string]int{"collision": 0, "reuse": 1, "related": 2}
			return priority[candidates[i].Verdict.ID] < priority[candidates[j].Verdict.ID]
		}
		if candidates[i].Score != candidates[j].Score {
			return candidates[i].Score > candidates[j].Score
		}
		return candidates[i].Task.TaskID < candidates[j].Task.TaskID
	})

	decisionID := "new"
	detail := "No configured related-work evidence was returned."
	for _, candidate := range candidates {
		if candidate.Verdict.ID == "collision" {
			decisionID = "collision"
			detail = "Another active task shares an exact durable artifact binding; inspect both before changing state."
			break
		}
	}
	if decisionID != "collision" && len(worklinksOutcome.links) > 0 {
		decisionID = "resume"
		detail = "This task already has durable work bindings; resume them before creating replacement work."
	} else if decisionID == "new" && len(candidates) > 0 {
		decisionID = candidates[0].Verdict.ID
		detail = "Related task evidence is available for reuse or comparison."
	} else if decisionID == "new" && len(history) > 0 {
		decisionID = "related"
		detail = "Related history evidence is available; inspect it before starting new work."
	}

	passes := []DiscoveryPass{
		{ID: "exact_bindings", Label: "Exact bindings", Complete: true, Count: len(worklinksOutcome.links), Detail: "Stable task identity and exact worklinks."},
		{ID: "structural_context", Label: "Structural context", Complete: true, Count: len(snapshotOutcome.snapshot.Tasks), Detail: "Acceptance and dependency closure."},
		{ID: "task_search", Label: "Ranked task search", Complete: taskSearchOutcome.err == nil, Count: len(searchCandidates)},
		{ID: "artifact_hydration", Label: "Artifact hydration", Complete: len(hydrationErrors) == 0, Count: len(hydrated)},
		{ID: "history_search", Label: "History search", Complete: historyComplete, Count: len(history)},
	}
	if taskSearchOutcome.err != nil {
		passes[2].Error = taskSearchOutcome.err.Error()
	}
	if len(hydrationErrors) > 0 {
		passes[3].Error = strings.Join(hydrationErrors, "; ")
	}
	if len(historyErrors) > 0 {
		passes[4].Error = strings.Join(historyErrors, "; ")
	}
	complete := true
	for _, pass := range passes {
		complete = complete && pass.Complete
	}
	contextTasks := append([]Task(nil), snapshotOutcome.snapshot.Tasks...)
	sort.SliceStable(contextTasks, func(i, j int) bool { return contextTasks[i].TaskID < contextTasks[j].TaskID })
	dependencies := append([]Dependency(nil), snapshotOutcome.snapshot.Dependencies...)
	sort.SliceStable(dependencies, func(i, j int) bool {
		if dependencies[i].TaskID != dependencies[j].TaskID {
			return dependencies[i].TaskID < dependencies[j].TaskID
		}
		return dependencies[i].DependsOnTaskID < dependencies[j].DependsOnTaskID
	})
	links := append([]Worklink(nil), worklinksOutcome.links...)
	sortWorklinks(links)
	return RelatedWorkPacket{
		TaskID: taskID, GeneratedAt: service.now().UTC(), ProjectID: snapshotOutcome.snapshot.ProjectID,
		Revision: snapshotOutcome.snapshot.Revision, ReadTimestamp: snapshotOutcome.snapshot.ReadTimestamp,
		StoreType: snapshotOutcome.snapshot.StoreType, Scope: snapshotOutcome.snapshot.Scope, Query: query,
		Complete: complete, Decision: service.discoveryVerdict(decisionID, detail), Task: *task,
		ContextTasks: contextTasks, Dependencies: dependencies, Worklinks: links, Candidates: candidates,
		History: history, Passes: passes,
	}, nil
}

func (service *Service) discoveryQuery(task Task) string {
	values := make([]string, 0, len(service.config.Discovery.QueryFields))
	for _, field := range service.config.Discovery.QueryFields {
		switch field {
		case "task_id":
			values = append(values, task.TaskID)
		case "title":
			values = append(values, task.Title)
		case "note":
			values = append(values, task.Note)
		case "pillar":
			values = append(values, task.Pillar)
		case "repo":
			values = append(values, task.Repo)
		case "owner":
			values = append(values, task.Owner)
		case "level":
			values = append(values, task.Level)
		}
	}
	seen := make(map[string]struct{})
	terms := make([]string, 0, service.config.Discovery.MaxQueryTerms)
	for _, value := range values {
		for _, token := range service.discoveryTokens(value) {
			if _, exists := seen[token]; exists {
				continue
			}
			seen[token] = struct{}{}
			terms = append(terms, token)
			if len(terms) == service.config.Discovery.MaxQueryTerms {
				return strings.Join(terms, " ")
			}
		}
	}
	return strings.Join(terms, " ")
}

func mergeTaskProjection(target *Task, source Task) {
	if target.Repo == "" {
		target.Repo = source.Repo
	}
	if target.SessionID == "" {
		target.SessionID = source.SessionID
	}
}

func (service *Service) discoveryTokens(value string) []string {
	stopWords := make(map[string]struct{}, len(service.config.Discovery.StopWords))
	for _, word := range service.config.Discovery.StopWords {
		stopWords[word] = struct{}{}
	}
	tokens := make([]string, 0)
	for _, token := range tokenize(value) {
		if len(token) < service.config.Discovery.MinTokenLength {
			continue
		}
		if _, stopped := stopWords[token]; stopped {
			continue
		}
		tokens = append(tokens, token)
	}
	return tokens
}

func (service *Service) hydrateCandidateWorklinks(ctx context.Context, candidates []Task) (map[string][]Worklink, []string) {
	limit := service.config.Discovery.HydrateLimit
	if limit > len(candidates) {
		limit = len(candidates)
	}
	if limit == 0 {
		return map[string][]Worklink{}, nil
	}
	type outcome struct {
		taskID string
		links  []Worklink
		err    error
	}
	jobs := make(chan string)
	results := make(chan outcome, limit)
	workers := service.config.Discovery.HydrateConcurrency
	if workers > limit {
		workers = limit
	}
	var group sync.WaitGroup
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			for taskID := range jobs {
				links, err := service.fleet.Worklinks(ctx, taskID)
				results <- outcome{taskID: taskID, links: links, err: err}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, candidate := range candidates[:limit] {
			select {
			case jobs <- candidate.TaskID:
			case <-ctx.Done():
				return
			}
		}
	}()
	group.Wait()
	close(results)
	hydrated := make(map[string][]Worklink, limit)
	errorsByTask := make([]string, 0)
	for result := range results {
		if result.err != nil {
			errorsByTask = append(errorsByTask, result.taskID+": "+result.err.Error())
			continue
		}
		sortWorklinks(result.links)
		hydrated[result.taskID] = result.links
	}
	sort.Strings(errorsByTask)
	return hydrated, errorsByTask
}

func (service *Service) scoreRelatedTask(root Task, rootLinks []Worklink, candidate Task, candidateLinks []Worklink) (int, []string, bool) {
	score := service.config.Discovery.Weights["search_hit"]
	reasons := make([]string, 0, 8)
	if service.config.Discovery.Weights["search_hit"] > 0 {
		reasons = append(reasons, "search_hit")
	}
	titleOverlap := service.relatedTokenOverlap(root.Title, candidate.Title)
	if titleOverlap > 0 {
		score += titleOverlap * service.config.Discovery.Weights["title_token_overlap"]
		reasons = append(reasons, fmt.Sprintf("title_token_overlap:%d", titleOverlap))
	}
	noteOverlap := service.relatedTokenOverlap(root.Note, candidate.Note)
	if noteOverlap > 0 {
		score += noteOverlap * service.config.Discovery.Weights["note_token_overlap"]
		reasons = append(reasons, fmt.Sprintf("note_token_overlap:%d", noteOverlap))
	}
	if root.Repo != "" && strings.EqualFold(root.Repo, candidate.Repo) {
		score += service.config.Discovery.Weights["same_repo"]
		reasons = append(reasons, "same_repo")
	}
	if root.ParentID != "" && root.ParentID == candidate.ParentID {
		score += service.config.Discovery.Weights["same_parent"]
		reasons = append(reasons, "same_parent")
	}
	if root.Pillar != "" && strings.EqualFold(root.Pillar, candidate.Pillar) {
		score += service.config.Discovery.Weights["same_pillar"]
		reasons = append(reasons, "same_pillar")
	}
	sharedArtifact := worklinksOverlap(rootLinks, candidateLinks, service.normalizedPath)
	if sharedArtifact {
		score += service.config.Discovery.Weights["shared_artifact"]
		reasons = append(reasons, "shared_artifact")
	}
	if service.isTerminal(candidate.Status) {
		score += service.config.Discovery.Weights["terminal"]
		reasons = append(reasons, "terminal")
	}
	return score, reasons, sharedArtifact
}

func (service *Service) relatedTokenOverlap(left, right string) int {
	leftTokens := make(map[string]struct{})
	for _, token := range service.discoveryTokens(left) {
		leftTokens[token] = struct{}{}
	}
	seen := make(map[string]struct{})
	overlap := 0
	for _, token := range service.discoveryTokens(right) {
		if _, duplicate := seen[token]; duplicate {
			continue
		}
		seen[token] = struct{}{}
		if _, matches := leftTokens[token]; matches {
			overlap++
		}
	}
	return overlap
}

func (service *Service) discoveryVerdict(id, detail string) DiscoveryVerdict {
	for _, verdict := range service.config.Discovery.Verdicts {
		if verdict.ID == id {
			return DiscoveryVerdict{ID: verdict.ID, Label: verdict.Label, Color: verdict.Color, Detail: detail}
		}
	}
	return DiscoveryVerdict{ID: id, Detail: detail}
}

func historyEvidenceKey(session SessionSummary) string {
	if session.ChunkID != "" {
		return "chunk:" + session.ChunkID
	}
	return strings.Join([]string{session.SessionID, session.FilePath, session.Operation, session.Summary}, "\x00")
}

func worklinksOverlap(left, right []Worklink, normalize func(string) string) bool {
	keys := make(map[string]struct{})
	for _, link := range left {
		if ref := normalize(link.ArtifactRef); ref != "" {
			keys[strings.ToLower(link.ArtifactType)+"\x00"+ref] = struct{}{}
		}
		if thread := strings.ToLower(strings.TrimSpace(link.Thread)); thread != "" {
			keys["thread\x00"+thread] = struct{}{}
		}
	}
	for _, link := range right {
		if ref := normalize(link.ArtifactRef); ref != "" {
			if _, exists := keys[strings.ToLower(link.ArtifactType)+"\x00"+ref]; exists {
				return true
			}
		}
		if thread := strings.ToLower(strings.TrimSpace(link.Thread)); thread != "" {
			if _, exists := keys["thread\x00"+thread]; exists {
				return true
			}
		}
	}
	return false
}

func sortWorklinks(links []Worklink) {
	sort.SliceStable(links, func(i, j int) bool {
		if links[i].ArtifactType != links[j].ArtifactType {
			return links[i].ArtifactType < links[j].ArtifactType
		}
		return links[i].ArtifactRef < links[j].ArtifactRef
	})
}

func (service *Service) TaskPrompt(ctx context.Context, taskID string) (TaskPrompt, error) {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" || len(taskID) > 512 {
		return TaskPrompt{}, errors.New("task_id is required")
	}
	related, err := service.RelatedWork(ctx, taskID)
	if err != nil {
		return TaskPrompt{}, fmt.Errorf("build related-work preflight: %w", err)
	}
	task := &related.Task
	snapshotOutcome := struct{ snapshot FleetSnapshot }{snapshot: FleetSnapshot{
		ProjectID: related.ProjectID, Revision: related.Revision, ReadTimestamp: related.ReadTimestamp,
		StoreType: related.StoreType, Scope: related.Scope, Tasks: related.ContextTasks, Dependencies: related.Dependencies,
	}}
	worklinksOutcome := struct{ links []Worklink }{links: related.Worklinks}

	contextTasks := append([]Task(nil), snapshotOutcome.snapshot.Tasks...)
	sort.SliceStable(contextTasks, func(i, j int) bool { return contextTasks[i].TaskID < contextTasks[j].TaskID })
	dependencies := append([]Dependency(nil), snapshotOutcome.snapshot.Dependencies...)
	sort.SliceStable(dependencies, func(i, j int) bool {
		if dependencies[i].TaskID != dependencies[j].TaskID {
			return dependencies[i].TaskID < dependencies[j].TaskID
		}
		return dependencies[i].DependsOnTaskID < dependencies[j].DependsOnTaskID
	})
	links := append([]Worklink(nil), worklinksOutcome.links...)
	sortWorklinks(links)

	var builder strings.Builder
	fmt.Fprintf(&builder, "Execute fleet-plan task %s.\n\n", taskID)
	builder.WriteString("TASK-GRAPH AUTHORITY — use this as the execution target\n")
	fmt.Fprintf(&builder, "Project: %s\nRevision: %s\nRead timestamp: %s\n", snapshotOutcome.snapshot.ProjectID, snapshotOutcome.snapshot.Revision, snapshotOutcome.snapshot.ReadTimestamp)
	var scope struct {
		Capped          bool     `json:"capped"`
		Complete        bool     `json:"complete"`
		MembershipCount int      `json:"membership_count"`
		MissingTaskIDs  []string `json:"missing_task_ids"`
	}
	if len(snapshotOutcome.snapshot.Scope) > 0 && json.Unmarshal(snapshotOutcome.snapshot.Scope, &scope) == nil {
		fmt.Fprintf(&builder, "Context scope: acceptance_dependency_closure; complete=%t; capped=%t; membership=%d\n", scope.Complete, scope.Capped, scope.MembershipCount)
		if len(scope.MissingTaskIDs) > 0 {
			fmt.Fprintf(&builder, "Unresolved context task ids: %s\n", strings.Join(scope.MissingTaskIDs, ", "))
		}
	}
	fmt.Fprintf(&builder, "\nTask id: %s\nTitle: %s\nStatus: %s\nOwner: %s\nPillar: %s\nLevel: %s\nParent: %s\nUpdated: %s\n", task.TaskID, task.Title, task.Status, task.Owner, task.Pillar, task.Level, task.ParentID, formatTime(task.UpdatedAt))
	if task.Note != "" {
		fmt.Fprintf(&builder, "Task contract / acceptance evidence:\n%s\n", task.Note)
	}
	if len(contextTasks) > 1 {
		builder.WriteString("\nContext tasks:\n")
		for _, candidate := range contextTasks {
			if candidate.TaskID == taskID {
				continue
			}
			fmt.Fprintf(&builder, "- %s [%s] %s; owner=%s; parent=%s\n", candidate.TaskID, candidate.Status, candidate.Title, candidate.Owner, candidate.ParentID)
		}
	}
	if len(dependencies) > 0 {
		builder.WriteString("Dependencies:\n")
		for _, dependency := range dependencies {
			fmt.Fprintf(&builder, "- %s -> %s (%s)\n", dependency.TaskID, dependency.DependsOnTaskID, dependency.Kind)
		}
	}
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
	builder.WriteString("\nRELATED-WORK PREFLIGHT — evidence only; never mutates fleet-plan\n")
	fmt.Fprintf(&builder, "Decision: %s (%s)\nQuery: %s\nCoverage complete: %t\n", related.Decision.Label, related.Decision.ID, related.Query, related.Complete)
	if related.Decision.Detail != "" {
		fmt.Fprintf(&builder, "Reason: %s\n", related.Decision.Detail)
	}
	for _, pass := range related.Passes {
		fmt.Fprintf(&builder, "- pass %s: complete=%t count=%d", pass.ID, pass.Complete, pass.Count)
		if pass.Error != "" {
			fmt.Fprintf(&builder, " error=%s", pass.Error)
		}
		builder.WriteByte('\n')
	}
	if len(related.Candidates) > 0 {
		builder.WriteString("Related task candidates:\n")
		for _, candidate := range related.Candidates {
			fmt.Fprintf(&builder, "- %s [%s] %s; verdict=%s; score=%d; reasons=%s\n", candidate.Task.TaskID, candidate.Task.Status, candidate.Task.Title, candidate.Verdict.ID, candidate.Score, strings.Join(candidate.Reasons, ","))
			for _, link := range candidate.Worklinks {
				fmt.Fprintf(&builder, "  - %s: %s\n", link.ArtifactType, link.ArtifactRef)
			}
		}
	}
	if len(related.History) > 0 {
		builder.WriteString("Related history evidence:\n")
		for _, session := range related.History {
			fmt.Fprintf(&builder, "- %s; project=%s; machine=%s; recorded=%s; summary=%s\n", session.SessionID, session.ProjectName, session.MachineID, formatTime(session.Timestamp), truncateText(session.Summary, service.config.View.SummaryPreviewCharacters))
		}
	}
	builder.WriteString("\nKeep this exact task id as the stable work identity. Before creating a branch, worktree, or replacement task, inspect every durable work link above and resume an existing binding when one exists. Read any linked thread transcript or indexed history before acting. Verify current repository and live task state, then carry this task through its stated evidence and completion contract.")
	return TaskPrompt{TaskID: taskID, GeneratedAt: service.now().UTC(), Text: builder.String()}, nil
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

func (service *Service) isTerminal(status string) bool {
	group := ""
	for _, candidate := range service.config.Status.Groups {
		if containsFold(candidate.Statuses, status) {
			group = candidate.ID
			break
		}
	}
	return contains(service.config.Status.TerminalGroupIDs, group)
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

func truncateText(value string, limit int) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return strings.TrimSpace(string(runes[:limit])) + "…"
}
