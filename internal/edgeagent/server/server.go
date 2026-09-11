// Package server is the edge HTTP layer exposed on 127.0.0.1:9101 so
// the Pi sidecar (and opskeeper-edge's own health checks) can drive
// the host without going through the geminio tunnel. The
// tunnel-based path (cloud ↔ edge) remains the authoritative
// control plane; the HTTP layer is the convenience surface for
// anything running on the same host as edge — Pi, debug curl, web UI.
//
// Route map (v1.7 skeleton — handlers below are wired where the
// underlying primitive is self-contained, stubbed where the
// primitive still needs target-host integration):
//
//   GET    /health                        liveness + version + edge_id
//   POST   /v1/edge/propose               record a proposal; emits an
//                                         audit event and (in P-5)
//                                         fans out to the reviewer
//                                         worker via tunnel.
//   GET    /v1/edge/audit?since=N&kind=K  paginated snapshot of the
//                                         local HMAC chain.
//   POST   /v1/edge/tools/bash            cmdpolicy-gated shell.
//                                         read-only by default; write
//                                         requires X-Opskeeper-Pi-Approval-Token.
//   POST   /v1/edge/tools/host_restart_service
//                                         restart a unit on the allow-list.
//                                         Always requires approval.
//   POST   /v1/edge/tools/host_files/check
//                                         cheap "is path X within the
//                                         host_files allow-list?" —
//                                         no I/O, used by Pi to
//                                         pre-validate before reading.
//   POST   /v1/edge/tools/host_files/find_large_files
//                                         run `find` over one allowed
//                                         path; returns top-N by
//                                         size (default top_n=20,
//                                         min_bytes=1MiB).
//   POST   /v1/edge/tools/host_files/du_summary
//                                         run `du` over one allowed
//                                         path; returns per-subpath
//                                         sizes + total (default
//                                         depth=1).
//   POST   /v1/edge/tools/host_files/stat_file
//                                         os.Lstat one allowed path;
//                                         pure Go, no subprocess.
//
// Stubs (deferred to later rounds): multi-path batches for the three
// read tools (the underlying RunFindOne / RunDuOne / RunStatOne
// already work per-path, so multi-path is a one-line wire-shape
// extension).
//
// Auth posture: bind to loopback only (no listen on 0.0.0.0). The
// Pi sidecar runs on the same host so loopback is the right trust
// boundary. Operators who want network access must put edge behind
// a unix socket + ssh or behind a reverse proxy that injects a
// bearer; we deliberately do NOT bake bearer auth into this layer
// because the threat model is "local process tries to mutate the
// host", which approval tokens already solve.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/vincent-wuhan/opskeeper/internal/edgeagent/audit"
	"github.com/vincent-wuhan/opskeeper/internal/edgeagent/cmdpolicy"
	"github.com/vincent-wuhan/opskeeper/internal/edgeagent/host_files"
	"github.com/vincent-wuhan/opskeeper/internal/edgeagent/restart_service"
)

// Server is the HTTP edge surface. Construct via New() and call
// ListenAndServe / Shutdown. Server is safe for concurrent use.
type Server struct {
	// BindAddr is the listen address. New() defaults to
	// "127.0.0.1:9101"; operators can override before ListenAndServe.
	BindAddr string

	// EdgeID identifies this edge in audit events + logs. Required.
	EdgeID string

	// Bash is the cmdpolicy sandbox for read-only and Pi-mode shell
	// calls. Required for /v1/edge/tools/bash.
	Bash *cmdpolicy.Sandbox

	// HostFiles is the path-validator for /v1/edge/tools/host_files/check.
	HostFiles *host_files.SandboxConfig

	// Restart is the unit allow-list + mocked-vs-real flag for
	// /v1/edge/tools/host_restart_service.
	Restart *restart_service.SandboxConfig

	// Audit is the local HMAC chain. Every mutating handler Appends
	// here before returning. Required.
	Audit *audit.Chain

	// Approvals is the in-memory cache of single-use approval tokens
	// granted by the cloud reviewer worker (P-5). When nil, mutating
	// handlers refuse every request (fail-closed). When non-nil, a
	// missing/invalid token yields HTTP 401 / 403 / 410.
	Approvals *cmdpolicy.ApprovalCache

	// Logger is the structured log sink. Nil → slog.Default().
	Logger *slog.Logger

	// Version is reported in /health. Empty → "dev".
	Version string

	// NowFn is the wall-clock source for audit timestamps. Nil →
	// time.Now (tests override).
	NowFn func() time.Time

	hs *http.Server
}

// New constructs a Server with BindAddr defaulted and Logger
// defaulted; caller still needs to set Bash/HostFiles/Restart/Audit
// before ListenAndServe.
func New() *Server {
	return &Server{
		BindAddr: "127.0.0.1:9101",
		Logger:   slog.Default(),
		Version:  "dev",
	}
}

// Handler returns the http.Handler with all routes registered.
// Exposed so tests can drive the server via httptest without
// binding a real socket.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/v1/edge/audit", s.handleAudit)
	mux.HandleFunc("/v1/edge/propose", s.handlePropose)
	mux.HandleFunc("/v1/edge/tools/bash", s.handleBash)
	mux.HandleFunc("/v1/edge/tools/host_restart_service", s.handleRestart)
	mux.HandleFunc("/v1/edge/tools/host_files/check", s.handleHostFilesCheck)
	mux.HandleFunc("/v1/edge/tools/host_files/find_large_files", s.handleHostFilesFindLargeFiles)
	mux.HandleFunc("/v1/edge/tools/host_files/du_summary", s.handleHostFilesDuSummary)
	mux.HandleFunc("/v1/edge/tools/host_files/stat_file", s.handleHostFilesStatFile)
	return mux
}

// ListenAndServe binds BindAddr (loopback enforced — see
// enforceLoopback) and serves until Shutdown is called or the
// process exits. Returns http.ErrServerClosed on clean shutdown.
func (s *Server) ListenAndServe() error {
	if err := s.enforceLoopback(s.BindAddr); err != nil {
		return err
	}
	s.hs = &http.Server{
		Addr:              s.BindAddr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	s.log().Info("edge http server starting",
		slog.String("addr", s.BindAddr),
		slog.String("edge_id", s.EdgeID))
	if err := s.hs.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Shutdown gracefully stops the server. ctx bounds the wait.
func (s *Server) Shutdown(ctx context.Context) error {
	if s.hs == nil {
		return nil
	}
	return s.hs.Shutdown(ctx)
}

// enforceLoopback refuses to bind anything outside 127.0.0.0/8 or
// ::1/128. Pi and the edge process share the host; loopback is the
// trust boundary. Operators who need network access must front the
// server with a unix socket + ssh tunnel — NOT relax this check.
func (s *Server) enforceLoopback(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("server: parse bind addr %q: %w", addr, err)
	}
	if host == "" {
		return fmt.Errorf("server: empty bind host in %q", addr)
	}
	// Strip IPv6 zone if present.
	if i := strings.LastIndex(host, "%"); i >= 0 {
		host = host[:i]
	}
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsLoopback() {
			return nil
		}
		return fmt.Errorf("server: refusing to bind non-loopback address %q (Pi sidecar uses loopback only)", addr)
	}
	// Hostname like "localhost" — resolve once and check.
	ips, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("server: resolve %q: %w", host, err)
	}
	for _, ip := range ips {
		if !ip.IsLoopback() {
			return fmt.Errorf("server: refusing to bind %q (resolves to non-loopback %s)", addr, ip)
		}
	}
	return nil
}

func (s *Server) log() *slog.Logger {
	if s.Logger == nil {
		return slog.Default()
	}
	return s.Logger
}

// writeJSON sets Content-Type, writes the status code, and emits
// the JSON body. Errors during encoding are returned via the
// ResponseWriter; the caller has already responded by that point,
// so we just log.
func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// Compile-time guards: if any of these wiring types disappear, the
// server package fails to compile, surfacing the regression
// immediately instead of at runtime.
var (
	_ = cmdpolicy.Sandbox{}
	_ = host_files.SandboxConfig{}
	_ = restart_service.SandboxConfig{}
	_ = audit.Chain{}
)