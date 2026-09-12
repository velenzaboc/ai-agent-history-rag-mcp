package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/velenzaboc/ai-agent-history-rag-mcp/internal/recovery"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "work-recovery-console: %v\n", err)
		os.Exit(1)
	}
}

func run(arguments []string) error {
	flags := flag.NewFlagSet("work-recovery-console", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	configPath := flags.String("config", "", "absolute path to the console JSON configuration")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 || *configPath == "" {
		return errors.New("exactly one --config path is required")
	}
	config, err := recovery.LoadConfig(*configPath)
	if err != nil {
		return err
	}
	fleet, err := recovery.NewMCPFleetClient(config.Fleet, config.RequestTimeout())
	if err != nil {
		return err
	}
	history, err := recovery.NewLegacyHistoryClient(config.History, config.RequestTimeout())
	if err != nil {
		return err
	}
	service, err := recovery.NewService(config, fleet, history, nil)
	if err != nil {
		return err
	}
	logger := log.New(os.Stderr, "work-recovery-console: ", log.LstdFlags|log.LUTC)
	server, err := recovery.NewServer(config, service, logger)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", config.Listen)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	defer listener.Close()
	httpServer := server.HTTPServer()
	serveResult := make(chan error, 1)
	go func() { serveResult <- httpServer.Serve(listener) }()
	logger.Printf("listening on http://%s%s", config.Listen, config.BasePath)
	shutdown := make(chan os.Signal, 1)
	signal.Notify(shutdown, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(shutdown)
	select {
	case err := <-serveResult:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	case <-shutdown:
		ctx, cancel := context.WithTimeout(context.Background(), config.ShutdownTimeout())
		defer cancel()
		if err := httpServer.Shutdown(ctx); err != nil {
			return err
		}
		if err := <-serveResult; !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
}
