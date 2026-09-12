package recovery

import (
	"context"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

const maxAPIRequestBytes int64 = 64 << 10

//go:embed web/*
var webAssets embed.FS

type Application interface {
	Dashboard(context.Context) (Dashboard, error)
	Search(context.Context, HistorySearch) (HistoryBatch, error)
	ResumePacket(context.Context, string, string) (ResumePacket, error)
}

type Server struct {
	config Config
	app    Application
	logger *log.Logger
	bearer string

	cacheMu  sync.Mutex
	cachedAt time.Time
	cached   Dashboard
}

func NewServer(config Config, app Application, logger *log.Logger) (*Server, error) {
	if err := config.validate(); err != nil {
		return nil, fmt.Errorf("server config: %w", err)
	}
	if app == nil {
		return nil, errors.New("server application is required")
	}
	if logger == nil {
		logger = log.New(io.Discard, "", 0)
	}
	bearer := ""
	if config.Access.Mode == "bearer_env" {
		bearer = strings.TrimSpace(os.Getenv(config.Access.BearerEnv))
		if bearer == "" || len(bearer) > 16<<10 || strings.ContainsAny(bearer, "\r\n") {
			return nil, errors.New("access bearer credential is unavailable or invalid")
		}
	}
	return &Server{config: config, app: app, logger: logger, bearer: bearer}, nil
}

func (server *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+server.route(""), server.serveIndex)
	mux.HandleFunc("GET "+server.route("assets/styles.css"), server.serveStyles)
	mux.HandleFunc("GET "+server.route("assets/responsive.css"), server.serveResponsiveStyles)
	mux.HandleFunc("GET "+server.route("assets/app.js"), server.serveScript)
	mux.HandleFunc("GET "+server.route("theme.css"), server.serveTheme)
	mux.HandleFunc("GET "+server.route("healthz"), server.serveHealth)
	mux.HandleFunc("GET "+server.route("api/dashboard"), server.serveDashboard)
	mux.HandleFunc("POST "+server.route("api/history/search"), server.serveSearch)
	mux.HandleFunc("GET "+server.route("api/resume"), server.serveResume)
	return server.fixedHeaders(server.authenticate(mux))
}

func (server *Server) HTTPServer() *http.Server {
	return &http.Server{
		Addr:              server.config.Listen,
		Handler:           server.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       server.config.RequestTimeout() + 5*time.Second,
		WriteTimeout:      server.config.RequestTimeout() + 5*time.Second,
		IdleTimeout:       90 * time.Second,
		MaxHeaderBytes:    32 << 10,
	}
}

func (server *Server) route(suffix string) string {
	base := server.config.BasePath
	if base != "/" {
		base += "/"
	}
	return base + suffix
}

func (server *Server) serveIndex(response http.ResponseWriter, _ *http.Request) {
	server.serveEmbedded(response, "web/index.html", "text/html; charset=utf-8")
}

func (server *Server) serveStyles(response http.ResponseWriter, _ *http.Request) {
	server.serveEmbedded(response, "web/styles.css", "text/css; charset=utf-8")
}

func (server *Server) serveResponsiveStyles(response http.ResponseWriter, _ *http.Request) {
	server.serveEmbedded(response, "web/responsive.css", "text/css; charset=utf-8")
}

func (server *Server) serveScript(response http.ResponseWriter, _ *http.Request) {
	server.serveEmbedded(response, "web/app.js", "text/javascript; charset=utf-8")
}

func (server *Server) serveEmbedded(response http.ResponseWriter, name, contentType string) {
	payload, err := webAssets.ReadFile(name)
	if err != nil {
		server.writeError(response, http.StatusNotFound, "not_found")
		return
	}
	response.Header().Set("Content-Type", contentType)
	response.Header().Set("Cache-Control", "no-cache")
	response.WriteHeader(http.StatusOK)
	_, _ = response.Write(payload)
}

func (server *Server) serveTheme(response http.ResponseWriter, _ *http.Request) {
	theme := server.config.View.Theme
	payload := fmt.Sprintf(":root{--background:%s;--surface:%s;--surface-alt:%s;--text:%s;--muted:%s;--accent:%s;--accent-alt:%s;--danger:%s;--warning:%s;--success:%s}", theme.Background, theme.Surface, theme.SurfaceAlt, theme.Text, theme.Muted, theme.Accent, theme.AccentAlt, theme.Danger, theme.Warning, theme.Success)
	response.Header().Set("Content-Type", "text/css; charset=utf-8")
	response.Header().Set("Cache-Control", "no-cache")
	_, _ = response.Write([]byte(payload))
}

func (server *Server) serveHealth(response http.ResponseWriter, _ *http.Request) {
	server.writeJSON(response, http.StatusOK, map[string]string{"status": "live"})
}

func (server *Server) serveDashboard(response http.ResponseWriter, request *http.Request) {
	fresh := request.URL.Query().Get("fresh") == "1"
	dashboard, err := server.dashboard(request.Context(), fresh)
	if err != nil {
		server.logger.Printf("dashboard refresh failed: %v", err)
		server.writeError(response, http.StatusServiceUnavailable, "dashboard_unavailable")
		return
	}
	server.writeJSON(response, http.StatusOK, dashboard)
}

func (server *Server) dashboard(ctx context.Context, fresh bool) (Dashboard, error) {
	server.cacheMu.Lock()
	defer server.cacheMu.Unlock()
	cacheDuration := time.Duration(server.config.View.CacheSeconds) * time.Second
	if !fresh && cacheDuration > 0 && !server.cachedAt.IsZero() && time.Since(server.cachedAt) < cacheDuration {
		return server.cached, nil
	}
	dashboard, err := server.app.Dashboard(ctx)
	if err != nil {
		return Dashboard{}, err
	}
	server.cached = dashboard
	server.cachedAt = time.Now()
	return dashboard, nil
}

func (server *Server) serveSearch(response http.ResponseWriter, request *http.Request) {
	var search HistorySearch
	if !decodeAPIRequest(response, request, &search) {
		return
	}
	batch, err := server.app.Search(request.Context(), search)
	if err != nil {
		server.logger.Printf("history search failed: %v", err)
		server.writeError(response, http.StatusBadGateway, "history_search_unavailable")
		return
	}
	server.writeJSON(response, http.StatusOK, batch)
}

func (server *Server) serveResume(response http.ResponseWriter, request *http.Request) {
	taskID := strings.TrimSpace(request.URL.Query().Get("task_id"))
	sessionID := strings.TrimSpace(request.URL.Query().Get("session_id"))
	if len(taskID) > 512 || len(sessionID) > 512 || sessionID == "" {
		server.writeError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	packet, err := server.app.ResumePacket(request.Context(), taskID, sessionID)
	if err != nil {
		server.logger.Printf("resume packet failed: %v", err)
		server.writeError(response, http.StatusBadGateway, "resume_packet_unavailable")
		return
	}
	server.writeJSON(response, http.StatusOK, packet)
}

func decodeAPIRequest(response http.ResponseWriter, request *http.Request, value any) bool {
	if request.ContentLength > maxAPIRequestBytes {
		writeStaticError(response, http.StatusRequestEntityTooLarge, "request_too_large")
		return false
	}
	body := http.MaxBytesReader(response, request.Body, maxAPIRequestBytes)
	defer body.Close()
	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		writeStaticError(response, http.StatusBadRequest, "invalid_request")
		return false
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		writeStaticError(response, http.StatusBadRequest, "invalid_request")
		return false
	}
	return true
}

func (server *Server) authenticate(next http.Handler) http.Handler {
	if server.config.Access.Mode == "none" {
		return next
	}
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		header := request.Header.Get("Authorization")
		const prefix = "Bearer "
		if !strings.HasPrefix(header, prefix) || len(header) != len(prefix)+len(server.bearer) || subtle.ConstantTimeCompare([]byte(header[len(prefix):]), []byte(server.bearer)) != 1 {
			server.writeError(response, http.StatusUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(response, request)
	})
}

func (server *Server) fixedHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Cache-Control", "no-store")
		response.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
		response.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
		response.Header().Set("Referrer-Policy", "no-referrer")
		response.Header().Set("X-Content-Type-Options", "nosniff")
		response.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(response, request)
	})
}

func (server *Server) writeJSON(response http.ResponseWriter, status int, value any) {
	payload, err := json.Marshal(value)
	if err != nil || int64(len(payload)) > server.config.View.MaxAPIResponseBytes {
		server.logger.Printf("response marshal failed or exceeded configured limit: %v", err)
		server.writeError(response, http.StatusInternalServerError, "response_unavailable")
		return
	}
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.WriteHeader(status)
	_, _ = response.Write(append(payload, '\n'))
}

func (server *Server) writeError(response http.ResponseWriter, status int, code string) {
	writeStaticError(response, status, code)
}

func writeStaticError(response http.ResponseWriter, status int, code string) {
	payload, _ := json.Marshal(map[string]string{"error": code})
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.WriteHeader(status)
	_, _ = response.Write(append(payload, '\n'))
}
