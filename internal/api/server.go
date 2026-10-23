// Package api is the HTTP layer: routing, decoding, and error mapping. All
// behaviour lives in the service package.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel/trace"

	"github.com/karthikponna/tryboxi/internal/api/middleware"
	"github.com/karthikponna/tryboxi/internal/api/sse"
	"github.com/karthikponna/tryboxi/internal/service"
	"github.com/karthikponna/tryboxi/internal/telemetry"
	"github.com/karthikponna/tryboxi/internal/version"
	"github.com/karthikponna/tryboxi/pkg/client"
)

// Server serves the API.
type Server struct {
	svc     *service.Manager
	traces  *telemetry.Recorder
	log     *slog.Logger
	mux     *http.ServeMux
	metrics *metrics
	spec    []byte

	mu      sync.Mutex
	clients map[string]service.Caller
}

// New builds the handler tree.
func New(svc *service.Manager, log *slog.Logger, openapi []byte, traces *telemetry.Recorder) *Server {
	s := &Server{svc: svc, log: log, mux: http.NewServeMux(), clients: map[string]service.Caller{}, spec: openapi, traces: traces}
	s.metrics = newMetrics(svc)
	s.routes()
	return s
}

// MetricsHandler serves only Prometheus metrics (no auth, read-only).
func (s *Server) MetricsHandler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", promhttp.HandlerFor(s.metrics.reg, promhttp.HandlerOpts{}))
	return mux
}

// Handler wraps the mux with auth and common middleware.
func (s *Server) Handler(tok *middleware.Token) http.Handler {
	return middleware.Common(s.log, version.Version, version.MinClient, middleware.Auth(tok, os.Getuid(), http.HandlerFunc(s.routeAndName)))
}

// routeAndName dispatches to the mux, then names the request span after the
// matched route (the pattern is only known once the mux has run).
func (s *Server) routeAndName(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
	span := trace.SpanFromContext(r.Context())
	if r.Pattern != "" {
		span.SetName("HTTP " + r.Pattern)
	}
	if v := r.PathValue("id"); strings.HasPrefix(v, "sbx_") {
		span.SetAttributes(telemetry.Sandbox(v))
	} else if strings.HasPrefix(v, "exe_") {
		span.SetAttributes(telemetry.Exec(v))
	}
}

// ConnContext attaches peer credentials for unix connections.
func ConnContext(ctx context.Context, c net.Conn) context.Context {
	if uid, pid, ok := middleware.PeerCred(c); ok {
		return middleware.WithPeer(ctx, middleware.Peer{UID: uid, PID: pid, Transport: "unix"})
	}
	return middleware.WithPeer(ctx, middleware.Peer{Transport: "tcp"})
}

func (s *Server) routes() {
	h := s.mux.HandleFunc
	h("GET /v1/health", s.health)
	h("GET /v1/capacity", s.capacity)
	h("GET /v1/openapi.yaml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/yaml")
		_, _ = w.Write(s.spec)
	})

	h("POST /v1/templates", s.createTemplate)
	h("GET /v1/templates", s.listTemplates)
	h("GET /v1/templates/{id}", s.getTemplate)
	h("GET /v1/templates/{id}/logs", s.templateLogs)

	h("POST /v1/sandboxes", s.createSandbox)
	h("GET /v1/sandboxes", s.listSandboxes)
	h("GET /v1/sandboxes/{id}", s.getSandbox)
	h("DELETE /v1/sandboxes/{id}", s.deleteSandbox)
	h("POST /v1/sandboxes/{id}/pause", s.simple(s.svc.PauseSandbox))
	h("POST /v1/sandboxes/{id}/resume", s.simple(s.svc.ResumeSandbox))
	h("POST /v1/sandboxes/{id}/keepalive", s.simple(s.svc.Keepalive))
	h("POST /v1/sandboxes/{id}/exec", s.exec)
	h("GET /v1/sandboxes/{id}/executions", s.listExecutions)
	h("PUT /v1/sandboxes/{id}/files", s.putFile)
	h("GET /v1/sandboxes/{id}/files", s.getFile)
	h("POST /v1/sandboxes/{id}/sync", s.sync)
	h("POST /v1/sandboxes/{id}/pull", s.pull)
	h("GET /v1/sandboxes/{id}/diff", s.diff)
	h("POST /v1/sandboxes/{id}/apply", s.apply)
	h("POST /v1/sandboxes/{id}/snapshots", s.checkpoint)
	h("POST /v1/sandboxes/{id}/fork", s.fork)
	h("POST /v1/sandboxes/{id}/restore", s.restore)
	h("POST /v1/sandboxes/{id}/ports", s.ports)
	h("GET /v1/sandboxes/{id}/network", s.getNetwork)
	h("POST /v1/sandboxes/{id}/network", s.updateNetwork)
	h("POST /v1/sandboxes/{id}/recover", s.recoverSandbox)

	h("GET /v1/executions/{id}", s.getExecution)
	h("GET /v1/executions/{id}/stream", s.streamExecution)
	h("GET /v1/executions/{id}/output", s.output)
	h("GET /v1/executions/{id}/grep", s.grep)
	h("POST /v1/executions/{id}/signal", s.signal)
	h("POST /v1/executions/{id}/stdin", s.stdin)

	h("GET /v1/snapshots", s.listSnapshots)
	h("GET /v1/artifacts", s.listArtifacts)
	h("GET /v1/artifacts/{id}/content", s.artifactContent)
	h("POST /v1/artifacts/{id}/undo", s.undo)
	h("GET /v1/events", s.events)
	h("GET /v1/traces", s.listTraces)
	h("GET /v1/secrets", s.listSecrets)
	h("POST /v1/secrets", s.putSecret)
	h("DELETE /v1/secrets/{name}", s.deleteSecret)
	h("POST /v1/gc", s.gc)
	s.mux.Handle("GET /metrics", promhttp.HandlerFor(s.metrics.reg, promhttp.HandlerOpts{}))
}

// ---- helpers ----

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func (s *Server) fail(w http.ResponseWriter, err error) {
	e := service.AsError(err)
	if e.RetryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int(e.RetryAfter.Seconds())))
	}
	if e.Status >= 500 && e.Code == "internal" {
		s.log.Warn("request failed", "err", err)
	}
	writeJSON(w, e.Status, client.ErrorBody{Error: client.ErrorDetail{Code: e.Code, Message: e.Message, Retryable: e.Retryable,
		RetryAfterS: int(e.RetryAfter.Seconds())}})
}

func decode(r *http.Request, v any) error {
	if r.ContentLength == 0 {
		return nil
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, 8<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil && !errors.Is(err, io.EOF) {
		return &service.Error{Code: "invalid_request", Status: 400, Message: "bad JSON: " + err.Error()}
	}
	return nil
}

func (s *Server) caller(r *http.Request) service.Caller {
	c := middleware.CallerFrom(r.Context())
	s.mu.Lock()
	defer s.mu.Unlock()
	if sc, ok := s.clients[c.Name]; ok {
		return sc
	}
	sc := s.svc.EnsureClient(r.Context(), c.Name, c.UID)
	s.clients[c.Name] = sc
	return sc
}

func (s *Server) simple(fn func(context.Context, string) (client.Sandbox, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		v, err := fn(r.Context(), r.PathValue("id"))
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, 200, v)
	}
}

func qint(r *http.Request, k string, def int64) int64 {
	v := r.URL.Query().Get(k)
	if v == "" {
		return def
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return def
	}
	return n
}

func qbool(r *http.Request, k string) bool {
	v := strings.ToLower(r.URL.Query().Get(k))
	return v == "1" || v == "true" || v == "yes"
}

// ---- handlers ----

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.svc.HealthInfo(r.Context()))
}

func (s *Server) capacity(w http.ResponseWriter, r *http.Request) {
	c, warm := s.svc.Capacity()
	writeJSON(w, 200, client.Capacity{VCPUTotal: c.VCPUTotal, VCPUFree: c.VCPUFree, MemTotalMB: c.MemTotalMB, MemFreeMB: c.MemFreeMB,
		Sandboxes: c.Sandboxes, MaxSandbox: c.MaxSandbox, PoolVMs: c.PoolVMs, QueueLen: c.QueueLen, Pressure: c.Pressure, Warm: warm})
}

func (s *Server) createTemplate(w http.ResponseWriter, r *http.Request) {
	var req client.TemplateRequest
	if err := decode(r, &req); err != nil {
		s.fail(w, err)
		return
	}
	t, err := s.svc.CreateTemplate(r.Context(), req)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, 202, t)
}

func (s *Server) listTemplates(w http.ResponseWriter, r *http.Request) {
	ts, err := s.svc.ListTemplates(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"templates": ts})
}

func (s *Server) getTemplate(w http.ResponseWriter, r *http.Request) {
	t, err := s.svc.GetTemplate(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, 200, t)
}

func (s *Server) templateLogs(w http.ResponseWriter, r *http.Request) {
	p, err := s.svc.TemplateLogPath(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	if !qbool(r, "follow") {
		b, _ := os.ReadFile(p)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write(b)
		return
	}
	sw := sse.New(w)
	var off int64
	for {
		f, err := os.Open(p)
		if err == nil {
			_, _ = f.Seek(off, io.SeekStart)
			b, _ := io.ReadAll(f)
			f.Close()
			if len(b) > 0 {
				off += int64(len(b))
				if sw.Event(strconv.FormatInt(off, 10), "log", string(b)) != nil {
					return
				}
			}
		}
		t, err := s.svc.GetTemplate(r.Context(), r.PathValue("id"))
		if err == nil && t.Status != "building" {
			_ = sw.Event("", "done", t)
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-time.After(time.Second):
		}
	}
}

func (s *Server) createSandbox(w http.ResponseWriter, r *http.Request) {
	var req client.CreateSandboxRequest
	if err := decode(r, &req); err != nil {
		s.fail(w, err)
		return
	}
	t0 := time.Now()
	sb, err := s.svc.CreateSandbox(r.Context(), s.caller(r), req)
	if err != nil {
		s.fail(w, err)
		return
	}
	if !sb.Reused {
		s.metrics.create.WithLabelValues(sb.BootedFrom).Observe(time.Since(t0).Seconds())
	}
	status := 201
	if sb.Reused {
		status = 200
	}
	writeJSON(w, status, sb)
}

func (s *Server) listSandboxes(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	clientID := ""
	if q.Get("mine") != "" || q.Get("session_key") != "" {
		clientID = s.caller(r).ClientID
	}
	list, err := s.svc.ListSandboxes(r.Context(), clientID, q.Get("session_key"), q.Get("state"), qbool(r, "all"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"sandboxes": list})
}

func (s *Server) getSandbox(w http.ResponseWriter, r *http.Request) {
	sb, err := s.svc.GetSandbox(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, 200, sb)
}

func (s *Server) deleteSandbox(w http.ResponseWriter, r *http.Request) {
	if err := s.svc.DestroySandbox(r.Context(), r.PathValue("id"), ""); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]string{"id": r.PathValue("id"), "state": "destroyed"})
}

func (s *Server) exec(w http.ResponseWriter, r *http.Request) {
	var req client.ExecRequest
	if err := decode(r, &req); err != nil {
		s.fail(w, err)
		return
	}
	e, err := s.svc.Exec(r.Context(), r.PathValue("id"), req, r.Header.Get("Idempotency-Key"))
	if err != nil {
		s.fail(w, err)
		return
	}
	s.metrics.execs.WithLabelValues(e.Status).Inc()
	status := 200
	if e.Status == "running" {
		status = 202
	}
	writeJSON(w, status, e)
}

func (s *Server) listExecutions(w http.ResponseWriter, r *http.Request) {
	list, err := s.svc.ListExecutions(r.Context(), r.PathValue("id"), r.URL.Query().Get("state"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"executions": list})
}

func (s *Server) putFile(w http.ResponseWriter, r *http.Request) {
	isTar := strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-tar")
	res, err := s.svc.PutFile(r.Context(), r.PathValue("id"), r.URL.Query().Get("path"), http.MaxBytesReader(w, r.Body, 1<<30), isTar)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, 200, res)
}

func (s *Server) getFile(w http.ResponseWriter, r *http.Request) {
	data, isTar, err := s.svc.GetFile(r.Context(), r.PathValue("id"), r.URL.Query().Get("path"), qbool(r, "tar"), qint(r, "max_bytes", 0))
	if err != nil {
		s.fail(w, err)
		return
	}
	if isTar {
		w.Header().Set("Content-Type", "application/x-tar")
	} else {
		w.Header().Set("Content-Type", "application/octet-stream")
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	_, _ = w.Write(data)
}

func (s *Server) sync(w http.ResponseWriter, r *http.Request) {
	res, err := s.svc.Sync(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, 200, res)
}

func (s *Server) pull(w http.ResponseWriter, r *http.Request) {
	var req client.PullRequest
	if err := decode(r, &req); err != nil {
		s.fail(w, err)
		return
	}
	res, err := s.svc.Pull(r.Context(), r.PathValue("id"), req)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, 200, res)
}

func (s *Server) diff(w http.ResponseWriter, r *http.Request) {
	res, err := s.svc.Diff(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, 200, res)
}

func (s *Server) apply(w http.ResponseWriter, r *http.Request) {
	var req client.ApplyRequest
	if err := decode(r, &req); err != nil {
		s.fail(w, err)
		return
	}
	res, err := s.svc.Apply(r.Context(), r.PathValue("id"), req)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, 200, res)
}

func (s *Server) checkpoint(w http.ResponseWriter, r *http.Request) {
	var req client.SnapshotRequest
	if err := decode(r, &req); err != nil {
		s.fail(w, err)
		return
	}
	snap, err := s.svc.Checkpoint(r.Context(), r.PathValue("id"), req.Label)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, 201, snap)
}

func (s *Server) fork(w http.ResponseWriter, r *http.Request) {
	var req client.ForkRequest
	if err := decode(r, &req); err != nil {
		s.fail(w, err)
		return
	}
	if req.Count == 0 {
		req.Count = 1
	}
	res, err := s.svc.Fork(r.Context(), s.caller(r), r.PathValue("id"), req.Count)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, 201, res)
}

func (s *Server) restore(w http.ResponseWriter, r *http.Request) {
	var req client.RestoreRequest
	if err := decode(r, &req); err != nil {
		s.fail(w, err)
		return
	}
	sb, err := s.svc.RestoreSandbox(r.Context(), s.caller(r), r.PathValue("id"), req.SnapshotID)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, 201, sb)
}

func (s *Server) ports(w http.ResponseWriter, r *http.Request) {
	var req client.PortRequest
	if err := decode(r, &req); err != nil {
		s.fail(w, err)
		return
	}
	res, err := s.svc.ForwardPort(r.Context(), r.PathValue("id"), req)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, 200, res)
}

func (s *Server) getNetwork(w http.ResponseWriter, r *http.Request) {
	info, err := s.svc.NetworkInfo(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, 200, info)
}

func (s *Server) updateNetwork(w http.ResponseWriter, r *http.Request) {
	var req client.NetworkUpdate
	if err := decode(r, &req); err != nil {
		s.fail(w, err)
		return
	}
	info, err := s.svc.UpdateNetwork(r.Context(), r.PathValue("id"), req)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, 200, info)
}

func (s *Server) recoverSandbox(w http.ResponseWriter, r *http.Request) {
	art, err := s.svc.Recover(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, 201, art)
}

func (s *Server) getExecution(w http.ResponseWriter, r *http.Request) {
	e, err := s.svc.GetExecution(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, 200, e)
}

func (s *Server) streamExecution(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	after := uint64(sse.LastEventID(r))
	replay, live, cancel, err := s.svc.Subscribe(r.Context(), id, after)
	if err != nil {
		s.fail(w, err)
		return
	}
	defer cancel()
	sw := sse.New(w)
	send := func(seq uint64, stream string, data []byte) error {
		return sw.Event(strconv.FormatUint(seq, 10), stream, map[string]string{"data": strings.ToValidUTF8(string(data), "\uFFFD")})
	}
	for _, f := range replay {
		if send(f.Seq, f.Stream, f.Data) != nil {
			return
		}
	}
	keep := time.NewTicker(15 * time.Second)
	defer keep.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-keep.C:
			if sw.Comment("keepalive") != nil {
				return
			}
		case f, ok := <-live:
			if !ok {
				// Wait briefly for the exit to be recorded, then send it.
				for i := 0; i < 50; i++ {
					e, err := s.svc.GetExecution(r.Context(), id)
					if err != nil || e.Status != "running" {
						_ = sw.Event("", "exit", e)
						return
					}
					time.Sleep(100 * time.Millisecond)
				}
				return
			}
			if send(f.Seq, f.Stream, f.Data) != nil {
				return
			}
		}
	}
}

func (s *Server) output(w http.ResponseWriter, r *http.Request) {
	res, err := s.svc.Output(r.Context(), r.PathValue("id"), r.URL.Query().Get("stream"), qint(r, "offset", 0), qint(r, "limit", 64<<10))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, 200, res)
}

func (s *Server) grep(w http.ResponseWriter, r *http.Request) {
	res, err := s.svc.Grep(r.Context(), r.PathValue("id"), r.URL.Query().Get("stream"), r.URL.Query().Get("pattern"), int(qint(r, "max", 200)))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, 200, res)
}

func (s *Server) signal(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Signal int `json:"signal"`
	}
	if err := decode(r, &req); err != nil {
		s.fail(w, err)
		return
	}
	if err := s.svc.Signal(r.Context(), r.PathValue("id"), req.Signal); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) stdin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Data  string `json:"data"`
		Close bool   `json:"close"`
	}
	if err := decode(r, &req); err != nil {
		s.fail(w, err)
		return
	}
	if err := s.svc.WriteStdin(r.Context(), r.PathValue("id"), []byte(req.Data), req.Close); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) listSnapshots(w http.ResponseWriter, r *http.Request) {
	list, err := s.svc.ListSnapshots(r.Context(), r.URL.Query().Get("sandbox_id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"snapshots": list})
}

func (s *Server) listArtifacts(w http.ResponseWriter, r *http.Request) {
	list, err := s.svc.ListArtifacts(r.Context(), r.URL.Query().Get("sandbox_id"), r.URL.Query().Get("kind"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"artifacts": list})
}

func (s *Server) artifactContent(w http.ResponseWriter, r *http.Request) {
	p, kind, err := s.svc.ArtifactPath(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	f, err := os.Open(p)
	if err != nil {
		s.fail(w, err)
		return
	}
	defer f.Close()
	ct := "application/octet-stream"
	switch kind {
	case "diff":
		ct = "text/x-diff"
	case "backup", "tar", "recovery":
		ct = "application/x-tar"
	}
	w.Header().Set("Content-Type", ct)
	_, _ = io.Copy(w, f)
}

func (s *Server) undo(w http.ResponseWriter, r *http.Request) {
	res, err := s.svc.Undo(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, 200, res)
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	after := sse.LastEventID(r)
	past, live, cancel := s.svc.Events().Subscribe(r.Context(), after, r.URL.Query().Get("sandbox_id"))
	defer cancel()
	sw := sse.New(w)
	var last int64 = after
	for _, e := range past {
		if sw.Event(strconv.FormatInt(e.ID, 10), e.Type, e) != nil {
			return
		}
		last = e.ID
	}
	keep := time.NewTicker(15 * time.Second)
	defer keep.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-keep.C:
			if sw.Comment("keepalive") != nil {
				return
			}
		case e := <-live:
			if e.ID != 0 && e.ID <= last {
				continue
			}
			if sw.Event(strconv.FormatInt(e.ID, 10), e.Type, e) != nil {
				return
			}
		}
	}
}

func (s *Server) listTraces(w http.ResponseWriter, r *http.Request) {
	var traces []telemetry.Trace
	if s.traces != nil {
		traces = s.traces.Traces(r.URL.Query().Get("sandbox_id"), r.URL.Query().Get("exec_id"), int(qint(r, "limit", 20)))
	}
	if traces == nil {
		traces = []telemetry.Trace{}
	}
	writeJSON(w, 200, map[string]any{"traces": traces})
}

func (s *Server) listSecrets(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"secrets": s.svc.ListSecrets(r.Context())})
}

func (s *Server) putSecret(w http.ResponseWriter, r *http.Request) {
	var req client.SecretRequest
	if err := decode(r, &req); err != nil {
		s.fail(w, err)
		return
	}
	info, err := s.svc.PutSecret(r.Context(), req)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, 201, info)
}

func (s *Server) deleteSecret(w http.ResponseWriter, r *http.Request) {
	if err := s.svc.DeleteSecret(r.Context(), r.PathValue("name")); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]string{"deleted": r.PathValue("name")})
}

func (s *Server) gc(w http.ResponseWriter, r *http.Request) {
	var req client.GCRequest
	if err := decode(r, &req); err != nil {
		s.fail(w, err)
		return
	}
	res, err := s.svc.GC(r.Context(), req.DryRun)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, 200, res)
}

// ---- metrics ----

type metrics struct {
	reg    *prometheus.Registry
	create *prometheus.HistogramVec
	execs  *prometheus.CounterVec
}

func newMetrics(svc *service.Manager) *metrics {
	reg := prometheus.NewRegistry()
	m := &metrics{
		reg: reg,
		create: prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "sandboxd_create_seconds", Help: "Sandbox create latency by source.",
			Buckets: []float64{.1, .25, .5, 1, 2, 5, 10, 30}}, []string{"source"}),
		execs: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "sandboxd_executions_total", Help: "Executions by final status."}, []string{"status"}),
	}
	reg.MustRegister(m.create, m.execs, telemetry.SpanSeconds, telemetry.NetEvents, telemetry.NetBytes)
	gauge := func(name, help string, f func() float64) {
		reg.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: name, Help: help}, f))
	}
	gauge("sandboxd_vcpu_free", "Free vCPU budget.", func() float64 { c, _ := svc.Capacity(); return c.VCPUFree })
	gauge("sandboxd_mem_free_mb", "Free memory budget (MB).", func() float64 { c, _ := svc.Capacity(); return float64(c.MemFreeMB) })
	gauge("sandboxd_sandboxes", "Running sandboxes (excluding pool).", func() float64 { c, _ := svc.Capacity(); return float64(c.Sandboxes) })
	gauge("sandboxd_pool_vms", "Warm pool VMs.", func() float64 { c, _ := svc.Capacity(); return float64(c.PoolVMs) })
	gauge("sandboxd_queue_length", "Requests waiting for capacity.", func() float64 { c, _ := svc.Capacity(); return float64(c.QueueLen) })
	return m
}
