package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/velenzaboc/ai-agent-history-rag-mcp/internal/history/auth"
	"github.com/velenzaboc/ai-agent-history-rag-mcp/internal/history/config"
	"github.com/velenzaboc/ai-agent-history-rag-mcp/internal/history/durable"
	historyprocess "github.com/velenzaboc/ai-agent-history-rag-mcp/internal/history/process"
	"github.com/velenzaboc/ai-agent-history-rag-mcp/internal/history/store"
	"github.com/velenzaboc/ai-agent-history-rag-mcp/internal/runtimeconfig"
)

type fakeStoreExecutor struct {
	closed   bool
	closeErr error
}

func (e *fakeStoreExecutor) Query(context.Context, store.Statement) ([]store.Row, error) {
	return nil, nil
}
func (e *fakeStoreExecutor) Execute(context.Context, store.Statement) (int64, error) { return 0, nil }
func (e *fakeStoreExecutor) Apply(context.Context, store.Mutation) error             { return nil }
func (e *fakeStoreExecutor) ReadWrite(context.Context, func(store.Transaction) error) error {
	return nil
}
func (e *fakeStoreExecutor) UpdateDDL(context.Context, []store.DDLStatement) error { return nil }
func (e *fakeStoreExecutor) Close() error {
	e.closed = true
	return e.closeErr
}

type fakeDaemonStore struct {
	statsErr error
	closed   bool
}

func (s *fakeDaemonStore) Stats(context.Context) (store.Stats, error) {
	return store.Stats{}, s.statsErr
}
func (s *fakeDaemonStore) Upsert(context.Context, []store.Chunk) error { return nil }
func (s *fakeDaemonStore) Close() error                                { s.closed = true; return nil }

func isolatedDaemonListener(t *testing.T) net.Listener {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("bind isolated daemon listener: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	return listener
}

func useDaemonListener(t *testing.T, listener net.Listener) func() (network, address string) {
	t.Helper()
	original := newTCPListener
	var gotNetwork, gotAddress string
	newTCPListener = func(network, address string) (net.Listener, error) {
		gotNetwork, gotAddress = network, address
		return listener, nil
	}
	t.Cleanup(func() { newTCPListener = original })
	return func() (string, string) { return gotNetwork, gotAddress }
}

func TestParseModeIsClosed(t *testing.T) {
	for command, want := range map[string]historyprocess.Mode{"start": historyprocess.Start, "supervise": historyprocess.Supervise} {
		got, err := parseMode(command)
		if err != nil || got != want {
			t.Fatalf("parseMode(%q) = %v, %v", command, got, err)
		}
	}
	for _, command := range []string{"", "stop", "restart", "START", "start "} {
		if _, err := parseMode(command); err == nil {
			t.Fatalf("parseMode(%q) accepted", command)
		}
	}
}

func TestModuleHonorsFleetFloors(t *testing.T) {
	payload, err := os.ReadFile("../../go.mod")
	if err != nil {
		t.Fatal(err)
	}
	checks := map[string]string{
		"Go toolchain": `(?m)^go 1\.27\.0$`,
		"x/sys":        `(?m)^require golang\.org/x/sys v0\.46\.0$`,
	}
	for name, pattern := range checks {
		if !regexp.MustCompile(pattern).Match(payload) {
			t.Fatalf("%s fleet floor missing from go.mod", name)
		}
	}
}

func TestRunRejectsMalformedInvocation(t *testing.T) {
	for _, arguments := range [][]string{nil, {"stop"}, {"start"}, {"start", "--unknown"}, {"start", "--config", "relative"}} {
		if err := run(arguments); err == nil {
			t.Fatalf("run(%q) accepted malformed invocation", arguments)
		}
	}
	if written, err := (ioDiscard{}).Write([]byte("flags")); err != nil || written != len("flags") {
		t.Fatalf("ioDiscard.Write() = %d, %v", written, err)
	}
}

func TestMainReportsGenericFailureInSubprocess(t *testing.T) {
	if os.Getenv("HISTORY_RAGD_TEST_MAIN") == "1" {
		main()
		return
	}
	command := exec.Command(os.Args[0], "-test.run=TestMainReportsGenericFailureInSubprocess")
	command.Env = append(os.Environ(), "HISTORY_RAGD_TEST_MAIN=1")
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatal("main accepted test-runner arguments as a daemon command")
	}
	if string(output) != "history-ragd: operation failed\n" {
		t.Fatalf("main output = %q", output)
	}
}

func TestDaemonExitCodeReflectsOnlyOperationResult(t *testing.T) {
	if got := daemonExitCode(func([]string) error { return nil }, []string{"start"}); got != 0 {
		t.Fatalf("daemonExitCode(success) = %d", got)
	}
	if got := daemonExitCode(func([]string) error { return os.ErrInvalid }, nil); got != 1 {
		t.Fatalf("daemonExitCode(failure) = %d", got)
	}
}

func TestOpenProductionStoreWiresClosedRuntimeToSpannerExecutor(t *testing.T) {
	oldLoad, oldExecutor, oldStore := loadProductionRuntime, newSpannerExecutor, newHistoryStore
	t.Cleanup(func() {
		loadProductionRuntime, newSpannerExecutor, newHistoryStore = oldLoad, oldExecutor, oldStore
	})
	runtime := runtimeconfig.Config{Spanner: runtimeconfig.SpannerTarget{Project: "fixture-project", Instance: "fixture-instance", Database: "fixture-database"}}
	loadProductionRuntime = func(func(string) string) (runtimeconfig.Config, error) { return runtime, nil }
	executor := &fakeStoreExecutor{}
	newSpannerExecutor = func(ctx context.Context, database string, got runtimeconfig.Config) (store.Executor, error) {
		if ctx == nil || database != "projects/fixture-project/instances/fixture-instance/databases/fixture-database" || got.Spanner != runtime.Spanner {
			t.Fatalf("Spanner executor inputs = ctx:%v database:%q runtime:%+v", ctx, database, got)
		}
		return executor, nil
	}
	created := &fakeDaemonStore{}
	newHistoryStore = func(configuration store.Config, got store.Executor) (daemonStore, error) {
		if got != store.Executor(executor) {
			t.Fatal("store received a different executor")
		}
		if err := configuration.Validate(); err != nil {
			t.Fatalf("production store configuration invalid: %v", err)
		}
		if configuration.Project != runtime.Spanner.Project || configuration.Database != runtime.Spanner.Database || configuration.ModelProject != runtime.Spanner.Project {
			t.Fatalf("production store configuration = %+v", configuration)
		}
		return created, nil
	}
	opened, err := openProductionStore(context.Background(), func(string) string { return "ignored" })
	if err != nil || opened != daemonStore(created) {
		t.Fatalf("openProductionStore() = %v, %v", opened, err)
	}
	if executor.closed {
		t.Fatal("openProductionStore closed executor after successful ownership transfer")
	}
}

func TestOpenProductionStoreClosesExecutorWhenStoreConstructionFails(t *testing.T) {
	oldLoad, oldExecutor, oldStore := loadProductionRuntime, newSpannerExecutor, newHistoryStore
	t.Cleanup(func() {
		loadProductionRuntime, newSpannerExecutor, newHistoryStore = oldLoad, oldExecutor, oldStore
	})
	loadProductionRuntime = func(func(string) string) (runtimeconfig.Config, error) {
		return runtimeconfig.Config{Spanner: runtimeconfig.SpannerTarget{Project: "fixture-project", Instance: "fixture-instance", Database: "fixture-database"}}, nil
	}
	executor := &fakeStoreExecutor{}
	newSpannerExecutor = func(context.Context, string, runtimeconfig.Config) (store.Executor, error) { return executor, nil }
	newHistoryStore = func(store.Config, store.Executor) (daemonStore, error) { return nil, os.ErrInvalid }
	if got, err := openProductionStore(context.Background(), func(string) string { return "ignored" }); err == nil || got != nil {
		t.Fatalf("openProductionStore() = %v, %v; want failure", got, err)
	}
	if !executor.closed {
		t.Fatal("openProductionStore did not close executor after store failure")
	}
}

func TestOpenProductionStorePropagatesEarlyAndCleanupFailures(t *testing.T) {
	oldLoad, oldExecutor, oldStore := loadProductionRuntime, newSpannerExecutor, newHistoryStore
	t.Cleanup(func() {
		loadProductionRuntime, newSpannerExecutor, newHistoryStore = oldLoad, oldExecutor, oldStore
	})
	runtime := runtimeconfig.Config{Spanner: runtimeconfig.SpannerTarget{Project: "fixture-project", Instance: "fixture-instance", Database: "fixture-database"}}
	for name, configure := range map[string]func(*fakeStoreExecutor){
		"runtime load": func(*fakeStoreExecutor) {
			loadProductionRuntime = func(func(string) string) (runtimeconfig.Config, error) { return runtimeconfig.Config{}, os.ErrNotExist }
		},
		"executor construction": func(*fakeStoreExecutor) {
			loadProductionRuntime = func(func(string) string) (runtimeconfig.Config, error) { return runtime, nil }
			newSpannerExecutor = func(context.Context, string, runtimeconfig.Config) (store.Executor, error) {
				return nil, os.ErrPermission
			}
		},
		"store cleanup": func(executor *fakeStoreExecutor) {
			loadProductionRuntime = func(func(string) string) (runtimeconfig.Config, error) { return runtime, nil }
			newSpannerExecutor = func(context.Context, string, runtimeconfig.Config) (store.Executor, error) { return executor, nil }
			newHistoryStore = func(store.Config, store.Executor) (daemonStore, error) { return nil, os.ErrInvalid }
			executor.closeErr = os.ErrPermission
		},
	} {
		t.Run(name, func(t *testing.T) {
			loadProductionRuntime, newSpannerExecutor, newHistoryStore = oldLoad, oldExecutor, oldStore
			executor := &fakeStoreExecutor{}
			configure(executor)
			if got, err := openProductionStore(context.Background(), func(string) string { return "ignored" }); err == nil || got != nil {
				t.Fatalf("openProductionStore() = %v, %v; want failure", got, err)
			}
			if name == "store cleanup" && !executor.closed {
				t.Fatal("openProductionStore did not attempt executor cleanup")
			}
		})
	}
}

func TestStoreReadinessUsesStatsAndFailsClosed(t *testing.T) {
	if err := (storeReadiness{}).Ready(context.Background()); err == nil {
		t.Fatal("store readiness accepted nil store")
	}
	storeDependency := &fakeDaemonStore{}
	readiness := storeReadiness{store: storeDependency}
	if readiness.Name() != "store" || readiness.Ready(context.Background()) != nil {
		t.Fatalf("store readiness = name:%q error:%v", readiness.Name(), readiness.Ready(context.Background()))
	}
	storeDependency.statsErr = os.ErrPermission
	if err := readiness.Ready(context.Background()); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("store readiness error = %v", err)
	}
}

func TestWatcherReadinessRequiresLiveProducer(t *testing.T) {
	if err := (watcherReadiness{}).Ready(context.Background()); err == nil {
		t.Fatal("watcher readiness accepted nil producer")
	}
	if (watcherReadiness{}).Name() != "watcher" {
		t.Fatal("watcher readiness name drifted")
	}
}

func TestRunUsesProductionStoreOnlyWhenRuntimeContractIsDeclared(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	originalWorkingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(filepath.Dir(executable)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(originalWorkingDirectory) })
	state := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(state, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		StateDir: state, Listen: "127.0.0.1:4680",
		PIDFile: filepath.Join(state, "daemon.pid"), AuthStateFile: filepath.Join(state, "auth.json"),
		CheckoutRoot: filepath.Dir(executable), Executable: executable,
	}
	payload, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(filepath.Dir(state), "daemon.json")
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	oldStore := newProductionStore
	t.Cleanup(func() { newProductionStore = oldStore })
	called := false
	newProductionStore = func(context.Context, func(string) string) (daemonStore, error) {
		called = true
		return nil, os.ErrPermission
	}
	t.Setenv("CLAUDE_HISTORY_RAG_RUNTIME_CONTRACT", "production")
	if err := run([]string{"start", "--config", path}); !errors.Is(err, os.ErrPermission) || !called {
		t.Fatalf("run(production) = %v, store_called=%t", err, called)
	}
}

func TestRunStartsRealWatcherDependencyWhenProductionRootsConfigured(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	checkout := filepath.Dir(executable)
	originalWorkingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(checkout); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(originalWorkingDirectory) })
	state := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(state, 0o700); err != nil {
		t.Fatal(err)
	}
	roots := make([]string, 0, 6)
	for _, name := range []string{"claude", "codex", "gemini", "antigravity", "chatgpt", "claude-app"} {
		root := filepath.Join(filepath.Dir(state), name)
		if err := os.Mkdir(root, 0o700); err != nil {
			t.Fatal(err)
		}
		roots = append(roots, root)
	}
	cfg := config.Config{StateDir: state, Listen: "127.0.0.1:4680", WatchRoots: roots, PIDFile: filepath.Join(state, "daemon.pid"), AuthStateFile: filepath.Join(state, "auth.json"), CheckoutRoot: checkout, Executable: executable}
	payload, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(filepath.Dir(state), "daemon.json")
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	originalStore := newProductionStore
	newProductionStore = func(context.Context, func(string) string) (daemonStore, error) { return &fakeDaemonStore{}, nil }
	t.Cleanup(func() { newProductionStore = originalStore })
	t.Setenv("CLAUDE_HISTORY_RAG_RUNTIME_CONTRACT", "production")
	shutdown := make(chan os.Signal, 1)
	originalShutdown := newShutdownSignal
	newShutdownSignal = func() (<-chan os.Signal, func()) { return shutdown, func() {} }
	t.Cleanup(func() { newShutdownSignal = originalShutdown })
	listener := isolatedDaemonListener(t)
	listenerRequest := useDaemonListener(t, listener)
	done := make(chan error, 1)
	go func() { done <- run([]string{"start", "--config", path}) }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		response, requestErr := http.Get("http://" + listener.Addr().String() + "/health")
		if requestErr == nil {
			_ = response.Body.Close()
			if response.StatusCode != http.StatusOK {
				t.Fatalf("health status = %d", response.StatusCode)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("daemon did not become healthy: %v", requestErr)
		}
		time.Sleep(10 * time.Millisecond)
	}
	shutdown <- os.Interrupt
	if err := <-done; err != nil {
		t.Fatalf("run = %v", err)
	}
	if network, address := listenerRequest(); network != "tcp" || address != "127.0.0.1:4680" {
		t.Fatalf("daemon listener request = %q %q, want production fixed endpoint", network, address)
	}
}

func TestRunStartRecognizesExactExistingDaemonIdentity(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	checkout := filepath.Dir(executable)
	originalWorkingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(checkout); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(originalWorkingDirectory) })
	state := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(state, 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := durable.OpenRoot(state)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	ops := historyprocess.NewSystemOps()
	record, err := historyprocess.CurrentRecord(ops, executable, checkout)
	if err != nil {
		t.Fatal(err)
	}
	controller, err := historyprocess.NewController(root, "daemon.pid", ops, historyprocess.DefaultSleep)
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.WriteRecord(record); err != nil {
		t.Fatal(err)
	}
	t.Setenv(pskEnvironment, "daemon-test-secret-with-enough-entropy")
	cfg := config.Config{StateDir: state, Listen: "127.0.0.1:4680", PIDFile: filepath.Join(state, "daemon.pid"), AuthStateFile: filepath.Join(state, "auth.json"), AuthEnabled: true, CheckoutRoot: checkout, Executable: executable}
	payload, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(filepath.Dir(state), "daemon.json")
	if err := os.WriteFile(configPath, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"start", "--config", configPath}); err != nil {
		t.Fatalf("run(start existing record) = %v", err)
	}
	if exists, err := root.Exists("daemon.pid"); err != nil || !exists {
		t.Fatalf("run(start) changed exact live record: %v, %v", exists, err)
	}
}

func TestRunRefusesEnabledAuthenticationWithoutCredential(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(state, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{StateDir: state, Listen: "127.0.0.1:4680", PIDFile: filepath.Join(state, "daemon.pid"), AuthStateFile: filepath.Join(state, "auth.json"), AuthEnabled: true, CheckoutRoot: filepath.Dir(executable), Executable: executable}
	payload, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(filepath.Dir(state), "daemon.json")
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(pskEnvironment, "")
	if err := run([]string{"start", "--config", path}); err == nil {
		t.Fatal("run accepted enabled authentication without a credential")
	}
}

func TestRunRefusesCredentialThatDoesNotMatchExistingAuthState(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(state, 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := durable.OpenRoot(state)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := auth.NewManager(root, "auth.json", bytes.NewReader(bytes.Repeat([]byte("r"), 32)))
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Initialize("stored-secret-with-enough-entropy"); err != nil {
		t.Fatal(err)
	}
	if err := root.Close(); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{StateDir: state, Listen: "127.0.0.1:4680", PIDFile: filepath.Join(state, "daemon.pid"), AuthStateFile: filepath.Join(state, "auth.json"), AuthEnabled: true, CheckoutRoot: filepath.Dir(executable), Executable: executable}
	payload, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(filepath.Dir(state), "daemon.json")
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(pskEnvironment, "different-secret-with-enough-entropy")
	if err := run([]string{"start", "--config", path}); err == nil {
		t.Fatal("run accepted credential that did not match durable auth state")
	}
}

func TestRunFailsBeforeWritingPIDWhenLoopbackPortIsOccupied(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	checkout := filepath.Dir(executable)
	originalWorkingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(checkout); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(originalWorkingDirectory) })
	state := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(state, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{StateDir: state, Listen: "127.0.0.1:4680", PIDFile: filepath.Join(state, "daemon.pid"), AuthStateFile: filepath.Join(state, "auth.json"), CheckoutRoot: checkout, Executable: executable}
	payload, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(filepath.Dir(state), "daemon.json")
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	listener := isolatedDaemonListener(t)
	originalListener := newTCPListener
	newTCPListener = func(network, address string) (net.Listener, error) {
		if network != "tcp" || address != cfg.Listen {
			return nil, errors.New("daemon listener request drifted from configuration")
		}
		return net.Listen(network, listener.Addr().String())
	}
	t.Cleanup(func() { newTCPListener = originalListener })
	if err := run([]string{"start", "--config", path}); err == nil {
		t.Fatal("run succeeded while its fixed loopback port was occupied")
	}
	if _, err := os.Stat(cfg.PIDFile); !os.IsNotExist(err) {
		t.Fatalf("run wrote a pid record before listener ownership: %v", err)
	}
}

func TestRunServesThenRemovesPIDOnTermination(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	checkout := filepath.Dir(executable)
	originalWorkingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(checkout); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(originalWorkingDirectory) })
	state := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(state, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{StateDir: state, Listen: "127.0.0.1:4680", PIDFile: filepath.Join(state, "daemon.pid"), AuthStateFile: filepath.Join(state, "auth.json"), CheckoutRoot: checkout, Executable: executable}
	payload, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(filepath.Dir(state), "daemon.json")
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	shutdown := make(chan os.Signal, 1)
	originalShutdownSignal := newShutdownSignal
	newShutdownSignal = func() (<-chan os.Signal, func()) {
		return shutdown, func() {}
	}
	t.Cleanup(func() { newShutdownSignal = originalShutdownSignal })
	listener := isolatedDaemonListener(t)
	listenerRequest := useDaemonListener(t, listener)
	done := make(chan error, 1)
	go func() { done <- run([]string{"start", "--config", path}) }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		connection, dialErr := net.DialTimeout("tcp", listener.Addr().String(), 50*time.Millisecond)
		if dialErr == nil {
			_ = connection.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("daemon did not become ready: %v", dialErr)
		}
		time.Sleep(10 * time.Millisecond)
	}
	shutdown <- os.Interrupt
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run(shutdown) = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("daemon did not shut down after SIGTERM")
	}
	if _, err := os.Stat(cfg.PIDFile); !os.IsNotExist(err) {
		t.Fatalf("daemon did not remove pid record: %v", err)
	}
	if network, address := listenerRequest(); network != "tcp" || address != "127.0.0.1:4680" {
		t.Fatalf("daemon listener request = %q %q, want production fixed endpoint", network, address)
	}
}
