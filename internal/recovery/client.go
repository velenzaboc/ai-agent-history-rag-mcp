package recovery

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"time"
)

var ErrSessionNotFound = errors.New("session history was not returned")

type FleetSource interface {
	Snapshot(context.Context) (FleetSnapshot, error)
	ScopedSnapshot(context.Context, string, string, int) (FleetSnapshot, error)
	Worklinks(context.Context, string) ([]Worklink, error)
	SessionWorklinks(context.Context, string) ([]Worklink, error)
	Search(context.Context, string, int) ([]Task, error)
}

func (client *MCPFleetClient) Worklinks(ctx context.Context, taskID string) ([]Worklink, error) {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" || len(taskID) > 512 {
		return nil, errors.New("task id is invalid")
	}
	var result struct {
		Worklinks []Worklink `json:"worklinks"`
	}
	if err := client.callTool(ctx, client.config.WorklinksTool, map[string]any{
		"project_id": client.config.ProjectID,
		"task_id":    taskID,
	}, &result); err != nil {
		return nil, err
	}
	for _, link := range result.Worklinks {
		if link.TaskID != "" && link.TaskID != taskID {
			return nil, fmt.Errorf("worklink %q belongs to unexpected task %q", link.ArtifactID, link.TaskID)
		}
		if link.ProjectID != "" && link.ProjectID != client.config.ProjectID {
			return nil, fmt.Errorf("worklink %q belongs to unexpected project %q", link.ArtifactID, link.ProjectID)
		}
	}
	return result.Worklinks, nil
}

func (client *MCPFleetClient) SessionWorklinks(ctx context.Context, sessionID string) ([]Worklink, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || len(sessionID) > 512 {
		return nil, errors.New("session id is invalid")
	}
	var result struct {
		Nodes []struct {
			NodeID         string `json:"node_id"`
			NodeKind       string `json:"node_kind"`
			ProjectID      string `json:"project_id"`
			Status         string `json:"status"`
			CurrentVersion struct {
				Contract json.RawMessage `json:"contract"`
				Payload  json.RawMessage `json:"payload"`
				Status   string          `json:"status"`
			} `json:"current_version"`
		} `json:"nodes"`
	}
	if err := client.callTool(ctx, client.config.SearchTool, map[string]any{
		"project_id": client.config.ProjectID,
		"query":      sessionID,
		"limit":      client.config.SessionLinkLimit,
	}, &result); err != nil {
		return nil, err
	}
	if len(result.Nodes) >= client.config.SessionLinkLimit {
		return nil, fmt.Errorf("session worklink search reached its configured result bound of %d; exact coverage is unknown", client.config.SessionLinkLimit)
	}
	links := make([]Worklink, 0, len(result.Nodes))
	seen := make(map[string]struct{}, len(result.Nodes))
	for _, node := range result.Nodes {
		if node.NodeKind != "artifact" {
			continue
		}
		if strings.EqualFold(node.Status, "archived") || strings.EqualFold(node.CurrentVersion.Status, "archived") {
			continue
		}
		if node.NodeID == "" || (node.ProjectID != "" && node.ProjectID != client.config.ProjectID) {
			return nil, errors.New("session worklink search returned an invalid artifact identity")
		}
		var contract struct {
			ArtifactRef      string `json:"artifact_ref"`
			ArtifactTypeKind string `json:"artifact_type_kind"`
			ProjectID        string `json:"project_id"`
		}
		var payload struct {
			TaskID         string `json:"task_id"`
			Thread         string `json:"thread"`
			SessionID      string `json:"session_id"`
			Note           string `json:"note"`
			ProjectID      string `json:"project_id"`
			FleetCreatedAt string `json:"fleet_created_at"`
		}
		if err := json.Unmarshal(node.CurrentVersion.Contract, &contract); err != nil {
			return nil, fmt.Errorf("decode session worklink contract %s: %w", node.NodeID, err)
		}
		if err := json.Unmarshal(node.CurrentVersion.Payload, &payload); err != nil {
			return nil, fmt.Errorf("decode session worklink payload %s: %w", node.NodeID, err)
		}
		if !strings.EqualFold(contract.ArtifactRef, sessionID) && !strings.EqualFold(payload.Thread, sessionID) && !strings.EqualFold(payload.SessionID, sessionID) {
			continue
		}
		projectID := node.ProjectID
		if projectID == "" {
			projectID = contract.ProjectID
		}
		if projectID == "" {
			projectID = payload.ProjectID
		}
		if projectID != client.config.ProjectID || payload.TaskID == "" || contract.ArtifactTypeKind == "" {
			return nil, fmt.Errorf("session worklink %q has an invalid project, task, or artifact type", node.NodeID)
		}
		if _, exists := seen[node.NodeID]; exists {
			continue
		}
		seen[node.NodeID] = struct{}{}
		links = append(links, Worklink{
			ArtifactID: node.NodeID, ArtifactRef: contract.ArtifactRef, ArtifactType: contract.ArtifactTypeKind,
			CreatedAt: payload.FleetCreatedAt, Note: payload.Note, ProjectID: projectID, SessionID: payload.SessionID, TaskID: payload.TaskID, Thread: payload.Thread,
		})
	}
	sortWorklinks(links)
	return links, nil
}

func (client *MCPFleetClient) Search(ctx context.Context, query string, limit int) ([]Task, error) {
	query = strings.TrimSpace(query)
	if query == "" || len(query) > 10000 || limit < 1 || limit > 100 {
		return nil, errors.New("task search request is invalid")
	}
	var result struct {
		Nodes []struct {
			NodeID         string `json:"node_id"`
			ProjectID      string `json:"project_id"`
			CurrentVersion struct {
				Payload json.RawMessage `json:"payload"`
			} `json:"current_version"`
		} `json:"nodes"`
	}
	if err := client.callTool(ctx, client.config.SearchTool, map[string]any{
		"project_id": client.config.ProjectID,
		"query":      query,
		"node_kind":  "task",
		"limit":      limit,
	}, &result); err != nil {
		return nil, err
	}
	tasks := make([]Task, 0, len(result.Nodes))
	for _, node := range result.Nodes {
		if len(node.CurrentVersion.Payload) == 0 {
			continue
		}
		var task Task
		if err := json.Unmarshal(node.CurrentVersion.Payload, &task); err != nil {
			return nil, fmt.Errorf("decode task search result %s: %w", node.NodeID, err)
		}
		var metadata struct {
			FleetUpdatedAt string `json:"fleet_updated_at"`
		}
		_ = json.Unmarshal(node.CurrentVersion.Payload, &metadata)
		if task.TaskID == "" {
			task.TaskID = node.NodeID
		}
		if task.ProjectID == "" {
			task.ProjectID = node.ProjectID
		}
		if task.UpdatedAt.IsZero() && metadata.FleetUpdatedAt != "" {
			task.UpdatedAt = parseTimestamp(metadata.FleetUpdatedAt)
		}
		if task.TaskID == "" || (task.ProjectID != "" && task.ProjectID != client.config.ProjectID) {
			return nil, errors.New("task search returned an invalid task identity")
		}
		tasks = append(tasks, task)
	}
	return tasks, nil
}

type HistorySource interface {
	Recent(context.Context) (HistoryBatch, error)
	Session(context.Context, string) (SessionSummary, error)
	Search(context.Context, HistorySearch) (HistoryBatch, error)
	Status(context.Context) (SourceStatus, error)
}

type MCPFleetClient struct {
	config         FleetConfig
	activeStatuses []string
	client         *http.Client
	nextID         atomic.Int64
}

func NewMCPFleetClient(config FleetConfig, activeStatuses []string, timeout time.Duration) (*MCPFleetClient, error) {
	if err := config.validate(); err != nil {
		return nil, fmt.Errorf("fleet client config: %w", err)
	}
	if timeout <= 0 {
		return nil, errors.New("fleet client timeout must be positive")
	}
	if config.TasksTool != "" && len(activeStatuses) == 0 {
		return nil, errors.New("fleet active overlay requires at least one active status")
	}
	return &MCPFleetClient{config: config, activeStatuses: append([]string(nil), activeStatuses...), client: boundedHTTPClient(timeout)}, nil
}

func (client *MCPFleetClient) Snapshot(ctx context.Context) (FleetSnapshot, error) {
	arguments := map[string]any{
		"project_id": client.config.ProjectID,
		"scope":      client.config.Scope,
		"limit":      client.config.Limit,
	}
	if client.config.RootTaskID != "" {
		arguments["root_task_id"] = client.config.RootTaskID
	}
	if client.config.TasksTool == "" {
		var snapshot FleetSnapshot
		if err := client.callTool(ctx, client.config.SnapshotTool, arguments, &snapshot); err != nil {
			return FleetSnapshot{}, err
		}
		if err := client.finishSnapshot(&snapshot, nil); err != nil {
			return FleetSnapshot{}, err
		}
		return snapshot, nil
	}
	type snapshotResult struct {
		snapshot FleetSnapshot
		err      error
	}
	type tasksResult struct {
		result taskListResult
		err    error
	}
	snapshotChannel := make(chan snapshotResult, 1)
	tasksChannel := make(chan tasksResult, 1)
	go func() {
		var snapshot FleetSnapshot
		err := client.callTool(ctx, client.config.SnapshotTool, arguments, &snapshot)
		snapshotChannel <- snapshotResult{snapshot: snapshot, err: err}
	}()
	go func() {
		var result taskListResult
		err := client.callTool(ctx, client.config.TasksTool, map[string]any{
			"project_id": client.config.ProjectID,
			"statuses":   client.activeStatuses,
			"limit":      client.config.ActiveLimit,
		}, &result)
		tasksChannel <- tasksResult{result: result, err: err}
	}()
	snapshotOutcome := <-snapshotChannel
	tasksOutcome := <-tasksChannel
	if snapshotOutcome.err != nil {
		return FleetSnapshot{}, snapshotOutcome.err
	}
	if tasksOutcome.err != nil {
		return FleetSnapshot{}, fmt.Errorf("read active task overlay: %w", tasksOutcome.err)
	}
	if err := client.finishSnapshot(&snapshotOutcome.snapshot, tasksOutcome.result.Tasks); err != nil {
		return FleetSnapshot{}, err
	}
	return snapshotOutcome.snapshot, nil
}

func (client *MCPFleetClient) ScopedSnapshot(ctx context.Context, scope, rootTaskID string, limit int) (FleetSnapshot, error) {
	scope = strings.TrimSpace(scope)
	rootTaskID = strings.TrimSpace(rootTaskID)
	if (scope != "execution_subtree" && scope != "acceptance_dependency_closure") || rootTaskID == "" || len(rootTaskID) > 512 {
		return FleetSnapshot{}, errors.New("scoped snapshot request is invalid")
	}
	if limit < 1 || limit > 100000 {
		return FleetSnapshot{}, errors.New("scoped snapshot limit is invalid")
	}
	var snapshot FleetSnapshot
	if err := client.callTool(ctx, client.config.SnapshotTool, map[string]any{
		"project_id":   client.config.ProjectID,
		"scope":        scope,
		"root_task_id": rootTaskID,
		"limit":        limit,
	}, &snapshot); err != nil {
		return FleetSnapshot{}, err
	}
	if err := client.finishSnapshot(&snapshot, nil); err != nil {
		return FleetSnapshot{}, err
	}
	return snapshot, nil
}

type taskListResult struct {
	Tasks []Task `json:"tasks"`
}

func (client *MCPFleetClient) finishSnapshot(snapshot *FleetSnapshot, activeTasks []Task) error {
	if snapshot.ProjectID != client.config.ProjectID {
		return fmt.Errorf("fleet snapshot project %q does not match configured project", snapshot.ProjectID)
	}
	snapshot.SnapshotTasks = len(snapshot.Tasks)
	byID := make(map[string]int, len(snapshot.Tasks))
	for index := range snapshot.Tasks {
		snapshot.Tasks[index].Projection = "snapshot"
		byID[snapshot.Tasks[index].TaskID] = index
	}
	for _, task := range activeTasks {
		if task.ProjectID != "" && task.ProjectID != client.config.ProjectID {
			return fmt.Errorf("active task %q belongs to unexpected project %q", task.TaskID, task.ProjectID)
		}
		if index, exists := byID[task.TaskID]; exists {
			task.Projection = "snapshot+active_overlay"
			snapshot.Tasks[index] = task
			continue
		}
		task.Projection = "active_overlay"
		byID[task.TaskID] = len(snapshot.Tasks)
		snapshot.Tasks = append(snapshot.Tasks, task)
	}
	snapshot.ActiveTasks = len(activeTasks)
	return nil
}

func (client *MCPFleetClient) callTool(ctx context.Context, toolName string, arguments map[string]any, output any) error {
	requestBody := map[string]any{
		"jsonrpc": "2.0",
		"id":      client.nextID.Add(1),
		"method":  "tools/call",
		"params": map[string]any{
			"name":      toolName,
			"arguments": arguments,
		},
	}
	payload, err := json.Marshal(requestBody)
	if err != nil {
		return fmt.Errorf("encode fleet request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, client.config.Endpoint, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("construct fleet request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	response, err := client.client.Do(request)
	if err != nil {
		return fmt.Errorf("fleet request: %w", err)
	}
	defer response.Body.Close()
	body, err := readBounded(response.Body, client.config.MaxResponseBytes)
	if err != nil {
		return fmt.Errorf("read fleet response: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return fmt.Errorf("fleet response status %d", response.StatusCode)
	}
	rpcPayload, err := streamableJSON(body, response.Header.Get("Content-Type"))
	if err != nil {
		return fmt.Errorf("decode fleet transport: %w", err)
	}
	var envelope struct {
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
		Result struct {
			StructuredContent json.RawMessage `json:"structuredContent"`
			Content           []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rpcPayload, &envelope); err != nil {
		return fmt.Errorf("decode fleet RPC: %w", err)
	}
	if envelope.Error != nil {
		return fmt.Errorf("fleet RPC %d: %s", envelope.Error.Code, envelope.Error.Message)
	}
	if envelope.Result.IsError {
		return errors.New("fleet tool returned an error")
	}
	structured := envelope.Result.StructuredContent
	if len(structured) == 0 {
		for _, item := range envelope.Result.Content {
			if item.Type == "text" && json.Valid([]byte(item.Text)) {
				structured = []byte(item.Text)
				break
			}
		}
	}
	if len(structured) == 0 {
		return errors.New("fleet response has no structured content")
	}
	if err := json.Unmarshal(structured, output); err != nil {
		return fmt.Errorf("decode fleet tool %s: %w", toolName, err)
	}
	return nil
}

type LegacyHistoryClient struct {
	config HistoryConfig
	client *http.Client
	bearer string
}

func NewLegacyHistoryClient(config HistoryConfig, timeout time.Duration) (*LegacyHistoryClient, error) {
	if err := config.validate(); err != nil {
		return nil, fmt.Errorf("history client config: %w", err)
	}
	if timeout <= 0 {
		return nil, errors.New("history client timeout must be positive")
	}
	bearer := ""
	if config.Auth.Mode == "bearer_env" {
		bearer = strings.TrimSpace(os.Getenv(config.Auth.BearerEnv))
		if bearer == "" {
			return nil, errors.New("history bearer credential is unavailable")
		}
		if strings.ContainsAny(bearer, "\r\n") || len(bearer) > 16<<10 {
			return nil, errors.New("history bearer credential is invalid")
		}
	}
	return &LegacyHistoryClient{config: config, client: boundedHTTPClient(timeout), bearer: bearer}, nil
}

func (client *LegacyHistoryClient) Recent(ctx context.Context) (HistoryBatch, error) {
	request := map[string]any{"session_id": nil, "project_filter": nullableString(client.config.ProjectFilter), "count": client.config.RecentLimit}
	return client.sessionRequest(ctx, request, client.config.RecentLimit)
}

func (client *LegacyHistoryClient) Session(ctx context.Context, sessionID string) (SessionSummary, error) {
	if strings.TrimSpace(sessionID) == "" || len(sessionID) > 512 {
		return SessionSummary{}, errors.New("session id is invalid")
	}
	request := map[string]any{"session_id": sessionID, "project_filter": nullableString(client.config.ProjectFilter), "count": 1}
	batch, err := client.sessionRequest(ctx, request, 1)
	if err != nil {
		return SessionSummary{}, err
	}
	if len(batch.Sessions) == 0 {
		return SessionSummary{}, ErrSessionNotFound
	}
	return batch.Sessions[0], nil
}

func (client *LegacyHistoryClient) sessionRequest(ctx context.Context, request map[string]any, requested int) (HistoryBatch, error) {
	var response historyResponse
	if err := client.doJSON(ctx, http.MethodPost, client.config.SessionsPath, request, &response); err != nil {
		return HistoryBatch{}, err
	}
	return normalizeHistoryResponse(response, requested), nil
}

func (client *LegacyHistoryClient) Search(ctx context.Context, search HistorySearch) (HistoryBatch, error) {
	search.Query = strings.TrimSpace(search.Query)
	if search.Query == "" && search.FilePath == "" {
		return HistoryBatch{}, errors.New("history search query or file path is required")
	}
	if len(search.Query) > 10000 || len(search.FilePath) > 10000 {
		return HistoryBatch{}, errors.New("history search input is too large")
	}
	limit := search.Limit
	if limit <= 0 || limit > client.config.SearchLimit {
		limit = client.config.SearchLimit
	}
	projectFilter := search.ProjectFilter
	if projectFilter == "" {
		projectFilter = client.config.ProjectFilter
	}
	request := map[string]any{
		"query":          nullableString(search.Query),
		"project_filter": nullableString(projectFilter),
		"date_from":      nullableString(search.DateFrom),
		"date_to":        nullableString(search.DateTo),
		"limit":          limit,
	}
	requestPath := client.config.SearchPath
	if search.Files {
		requestPath = client.config.FilesPath
		request["file_path"] = nullableString(search.FilePath)
		request["operation_filter"] = nullableString(search.Operation)
	} else {
		request["use_hybrid"] = client.config.Search.UseHybrid
		request["enable_analysis"] = client.config.Search.EnableAnalysis
		request["enable_synthesis"] = client.config.Search.EnableSynthesis
		request["include_debug"] = client.config.Search.IncludeDebug
	}
	var response historyResponse
	if err := client.doJSON(ctx, http.MethodPost, requestPath, request, &response); err != nil {
		return HistoryBatch{}, err
	}
	return normalizeHistoryResponse(response, limit), nil
}

func (client *LegacyHistoryClient) Status(ctx context.Context) (SourceStatus, error) {
	var response map[string]any
	err := client.doJSON(ctx, http.MethodGet, client.config.StatusPath, nil, &response)
	status := SourceStatus{Name: "history", Healthy: err == nil, ObservedAt: time.Now().UTC()}
	if err != nil {
		status.Error = err.Error()
		return status, err
	}
	status.Detail = response
	return status, nil
}

func (client *LegacyHistoryClient) doJSON(ctx context.Context, method, requestPath string, input, output any) error {
	var body io.Reader
	if input != nil {
		payload, err := json.Marshal(input)
		if err != nil {
			return fmt.Errorf("encode history request: %w", err)
		}
		body = bytes.NewReader(payload)
	}
	endpoint, err := joinEndpoint(client.config.Endpoint, requestPath)
	if err != nil {
		return fmt.Errorf("construct history endpoint: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return fmt.Errorf("construct history request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if client.bearer != "" {
		request.Header.Set("Authorization", "Bearer "+client.bearer)
	}
	response, err := client.client.Do(request)
	if err != nil {
		return fmt.Errorf("history request: %w", err)
	}
	defer response.Body.Close()
	payload, err := readBounded(response.Body, client.config.MaxResponseBytes)
	if err != nil {
		return fmt.Errorf("read history response: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return fmt.Errorf("history response status %d", response.StatusCode)
	}
	if err := json.Unmarshal(payload, output); err != nil {
		return fmt.Errorf("decode history response: %w", err)
	}
	return nil
}

type historyResponse struct {
	Summaries []historyItem `json:"summaries"`
	Results   []historyItem `json:"results"`
	Count     int           `json:"count"`
	Error     any           `json:"error"`
}

type historyItem struct {
	ID          string  `json:"id"`
	Content     string  `json:"content"`
	ChunkType   string  `json:"chunk_type"`
	SessionID   string  `json:"session_id"`
	ProjectPath string  `json:"project_path"`
	ProjectName string  `json:"project_name"`
	Timestamp   string  `json:"timestamp"`
	FilePath    string  `json:"file_path"`
	Operation   string  `json:"operation"`
	MachineID   string  `json:"machine_id"`
	Score       float64 `json:"score"`
}

func normalizeHistoryResponse(response historyResponse, requested int) HistoryBatch {
	items := response.Summaries
	if len(items) == 0 {
		items = response.Results
	}
	sessions := make([]SessionSummary, 0, len(items))
	for _, item := range items {
		sessions = append(sessions, SessionSummary{
			ChunkID: item.ID, SessionID: item.SessionID, Summary: item.Content, ChunkType: item.ChunkType,
			ProjectPath: item.ProjectPath, ProjectName: item.ProjectName, Timestamp: parseTimestamp(item.Timestamp),
			FilePath: item.FilePath, Operation: item.Operation, MachineID: item.MachineID, Score: item.Score,
		})
	}
	return HistoryBatch{Sessions: sessions, Requested: requested, Returned: len(sessions), Partial: len(sessions) >= requested}
}

func boundedHTTPClient(timeout time.Duration) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DisableCompression = false
	return &http.Client{
		Transport: transport,
		Timeout:   timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("redirects are disabled")
		},
	}
}

func readBounded(reader io.Reader, limit int64) ([]byte, error) {
	payload, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(payload)) > limit {
		return nil, errors.New("response exceeds configured size limit")
	}
	return payload, nil
}

func streamableJSON(payload []byte, contentType string) ([]byte, error) {
	if !strings.Contains(strings.ToLower(contentType), "text/event-stream") {
		if !json.Valid(payload) {
			return nil, errors.New("response is not valid JSON")
		}
		return payload, nil
	}
	scanner := bufio.NewScanner(bytes.NewReader(payload))
	buffer := make([]byte, 64<<10)
	scanner.Buffer(buffer, len(payload)+1)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if json.Valid([]byte(data)) {
			return []byte(data), nil
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return nil, errors.New("event stream contains no JSON message")
}

func joinEndpoint(base, requestPath string) (string, error) {
	parsed, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	pathURL, err := url.Parse(requestPath)
	if err != nil {
		return "", err
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + pathURL.Path
	parsed.RawQuery = pathURL.RawQuery
	return parsed.String(), nil
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}
