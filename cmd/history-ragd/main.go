package main

import (
	"context"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/no13productions/ai-agent-history-rag-mcp/internal/history/api"
	"github.com/no13productions/ai-agent-history-rag-mcp/internal/history/auth"
	"github.com/no13productions/ai-agent-history-rag-mcp/internal/history/config"
	"github.com/no13productions/ai-agent-history-rag-mcp/internal/history/durable"
	historyprocess "github.com/no13productions/ai-agent-history-rag-mcp/internal/history/process"
	"github.com/no13productions/ai-agent-history-rag-mcp/internal/history/spannerexec"
	"github.com/no13productions/ai-agent-history-rag-mcp/internal/history/store"
	"github.com/no13productions/ai-agent-history-rag-mcp/internal/history/watch"
	"github.com/no13productions/ai-agent-history-rag-mcp/internal/runtimeconfig"
)

const pskEnvironment = "CLAUDE_HISTORY_RAG_SERVER_PSK"

var newShutdownSignal = func() (<-chan os.Signal, func()) {
	shutdown := make(chan os.Signal, 1)
	signal.Notify(shutdown, syscall.SIGINT, syscall.SIGTERM)
	return shutdown, func() { signal.Stop(shutdown) }
}

// newTCPListener is replaceable only so daemon lifecycle tests can provide an
// already-bound loopback listener. Production always uses net.Listen and the
// configuration contract continues to own the fixed public endpoint.
var newTCPListener = net.Listen

type daemonStore interface {
	Stats(context.Context) (store.Stats, error)
	Upsert(context.Context, []store.Chunk) error
	Close() error
}

type storeReadiness struct{ store daemonStore }

func (storeReadiness) Name() string { return "store" }
func (r storeReadiness) Ready(ctx context.Context) error {
	if r.store == nil {
		return errors.New("Spanner store is required")
	}
	_, err := r.store.Stats(ctx)
	return err
}

type watcherReadiness struct{ producer *watch.Producer }

func (watcherReadiness) Name() string { return "watcher" }
func (r watcherReadiness) Ready(ctx context.Context) error {
	if r.producer == nil {
		return errors.New("watch producer is required")
	}
	return r.producer.Ready(ctx)
}

var (
	loadProductionRuntime = runtimeconfig.LoadProduction
	newProductionStore    = openProductionStore
	newSpannerExecutor    = func(ctx context.Context, database string, selector runtimeconfig.Config) (store.Executor, error) {
		return spannerexec.New(ctx, database, selector.GoogleCredentials)
	}
	newHistoryStore = func(configuration store.Config, executor store.Executor) (daemonStore, error) {
		return store.New(configuration, executor)
	}
)

func main() {
	if daemonExitCode(run, os.Args[1:]) != 0 {
		fmt.Fprintln(os.Stderr, "history-ragd: operation failed")
		os.Exit(1)
	}
}

func daemonExitCode(operation func([]string) error, arguments []string) int {
	if err := operation(arguments); err != nil {
		return 1
	}
	return 0
}

func run(arguments []string) error {
	if len(arguments) == 0 {
		return errors.New("command required")
	}
	mode, err := parseMode(arguments[0])
	if err != nil {
		return err
	}
	flags := flag.NewFlagSet("history-ragd", flag.ContinueOnError)
	flags.SetOutput(ioDiscard{})
	configPath := flags.String("config", "", "absolute path to the native daemon configuration")
	if err := flags.Parse(arguments[1:]); err != nil || flags.NArg() != 0 || *configPath == "" {
		return errors.New("exactly one --config path is required")
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	root, err := durable.OpenRoot(cfg.StateDir)
	if err != nil {
		return err
	}
	defer root.Close()

	var verifier api.Verifier
	if cfg.AuthEnabled {
		secret := os.Getenv(pskEnvironment)
		if secret == "" {
			return errors.New("authentication credential unavailable")
		}
		manager, err := auth.NewManager(root, filepath.Base(cfg.AuthStateFile), rand.Reader)
		if err != nil {
			return err
		}
		if err := manager.Initialize(secret); err != nil {
			return err
		}
		verifier = manager
	}

	var controller *historyprocess.Controller
	var current historyprocess.Record
	if !cfg.ContainerMode {
		ops := historyprocess.NewSystemOps()
		current, err = historyprocess.CurrentRecord(ops, cfg.Executable, cfg.CheckoutRoot)
		if err != nil {
			return err
		}
		controller, err = historyprocess.NewController(root, filepath.Base(cfg.PIDFile), ops, historyprocess.DefaultSleep)
		if err != nil {
			return err
		}
		shouldRun, err := controller.Prepare(context.Background(), mode, current, 15*time.Second, 5*time.Second)
		if err != nil {
			return err
		}
		if !shouldRun {
			fmt.Fprintln(os.Stdout, "history-ragd is already running")
			return nil
		}
	}

	var dependencies []api.Readiness
	var stopWatcher context.CancelFunc
	watchResult := make(chan error, 1)
	if strings.TrimSpace(os.Getenv("CLAUDE_HISTORY_RAG_RUNTIME_CONTRACT")) != "" {
		historyStore, err := newProductionStore(context.Background(), os.Getenv)
		if err != nil {
			return err
		}
		defer historyStore.Close()
		dependencies = append(dependencies, storeReadiness{store: historyStore})
		if len(cfg.WatchRoots) != 0 {
			producer, err := watch.NewProducer(watch.Registry{MachineID: "history-ragd", MaxBytes: watch.MaxSourceSnapshotBytes}, historyStore, cfg.WatchRoots, time.Second)
			if err != nil {
				return fmt.Errorf("construct source watcher: %w", err)
			}
			if err := producer.Scan(context.Background()); err != nil {
				_ = producer.Close()
				return fmt.Errorf("initial source scan: %w", err)
			}
			watchContext, cancelWatcher := context.WithCancel(context.Background())
			stopWatcher = cancelWatcher
			defer func() {
				stopWatcher()
				_ = producer.Close()
			}()
			go func() { watchResult <- producer.Run(watchContext) }()
			dependencies = append(dependencies, watcherReadiness{producer: producer})
		}
	}

	// Register termination handling before publishing listener readiness. A
	// supervisor may signal the daemon as soon as the port accepts connections;
	// registering afterward leaves a small process-termination race.
	shutdownSignal, stopShutdownSignal := newShutdownSignal()
	defer stopShutdownSignal()

	listener, err := newTCPListener("tcp", cfg.Listen)
	if err != nil {
		return err
	}
	defer listener.Close()
	if controller != nil {
		if err := controller.WriteRecord(current); err != nil {
			return err
		}
		defer controller.RemoveRecord(current)
	}

	// The production store is live when the explicit production contract is
	// present. Watcher readiness remains deliberately absent until source-watch
	// ownership is cut over, so the API remains deterministically not-ready.
	apiServer, err := api.New(api.Config{AuthEnabled: cfg.AuthEnabled}, verifier, dependencies)
	if err != nil {
		return err
	}
	httpServer := apiServer.HTTPServer(cfg.Listen)
	serveResult := make(chan error, 1)
	go func() {
		serveResult <- httpServer.Serve(listener)
	}()

	select {
	case err := <-serveResult:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	case <-shutdownSignal:
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(ctx); err != nil {
			return err
		}
		if err := <-serveResult; !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	case err := <-watchResult:
		if err != nil && !errors.Is(err, context.Canceled) {
			return fmt.Errorf("source watcher stopped: %w", err)
		}
		return nil
	}
}

func openProductionStore(ctx context.Context, getenv func(string) string) (daemonStore, error) {
	runtime, err := loadProductionRuntime(getenv)
	if err != nil {
		return nil, err
	}
	executor, err := newSpannerExecutor(ctx, runtime.Spanner.ResourceName(), runtime)
	if err != nil {
		return nil, fmt.Errorf("construct production Spanner executor: %w", err)
	}
	historyStore, err := newHistoryStore(productionStoreConfig(runtime), executor)
	if err != nil {
		if closeErr := executor.Close(); closeErr != nil {
			return nil, fmt.Errorf("construct production store: %w; close executor: %v", err, closeErr)
		}
		return nil, fmt.Errorf("construct production store: %w", err)
	}
	return historyStore, nil
}

func productionStoreConfig(runtime runtimeconfig.Config) store.Config {
	return store.Config{
		Project:                    runtime.Spanner.Project,
		Instance:                   runtime.Spanner.Instance,
		Database:                   runtime.Spanner.Database,
		ModelProject:               runtime.Spanner.Project,
		ModelLocation:              "us-central1",
		EmbeddingStrategy:          store.EmbeddingRemoteModel,
		RemoteModel:                store.RemoteModelName,
		EmbeddingModel:             store.EmbeddingModelName,
		EmbeddingDimension:         store.VectorDimension,
		DocumentTaskType:           store.TaskRetrievalDocument,
		QueryTaskType:              store.TaskRetrievalQuery,
		RemoteRPCBatch:             1,
		EnableFullText:             true,
		EnableANN:                  true,
		UseANN:                     true,
		VectorIndexLeaves:          1000,
		NumLeavesToSearch:          50,
		HybridCandidateLimit:       100,
		RRFK:                       60,
		MaxSearchLimit:             100,
		BackfillConcurrency:        8,
		BackfillBatch:              200,
		BackfillInterval:           time.Minute,
		BackfillMaxBatchesPerShard: 1000,
		StatsCacheTTL:              10 * time.Second,
	}
}

func parseMode(command string) (historyprocess.Mode, error) {
	switch command {
	case "start":
		return historyprocess.Start, nil
	case "supervise":
		return historyprocess.Supervise, nil
	default:
		return 0, errors.New("command must be start or supervise")
	}
}

type ioDiscard struct{}

func (ioDiscard) Write(payload []byte) (int, error) { return len(payload), nil }
