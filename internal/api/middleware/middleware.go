// Package middleware authenticates callers and guards against browser
// requests (CSRF, DNS rebinding).
package middleware

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

type ctxKey int

const (
	keyPeer ctxKey = iota
	keyCaller
	keyRequestID
)

// Peer is what we know about the connection.
type Peer struct {
	UID       int
	PID       int
	Transport string // unix | tcp
}

// WithPeer stores connection identity (set from http.Server.ConnContext).
func WithPeer(ctx context.Context, p Peer) context.Context { return context.WithValue(ctx, keyPeer, p) }

// PeerFrom returns the connection identity.
func PeerFrom(ctx context.Context) (Peer, bool) {
	p, ok := ctx.Value(keyPeer).(Peer)
	return p, ok
}

// Caller is the authenticated client.
type Caller struct {
	Name     string // e.g. "uid501:sandbox-mcp/cursor"
	UID      int
	Priority int
}

// CallerFrom returns the authenticated caller.
func CallerFrom(ctx context.Context) Caller {
	c, _ := ctx.Value(keyCaller).(Caller)
	return c
}

// RequestID returns the request id.
func RequestID(ctx context.Context) string {
	s, _ := ctx.Value(keyRequestID).(string)
	return s
}

// Token manages the bearer token for TCP / forwarded-socket access.
type Token struct{ hash [32]byte }

// LoadOrCreateToken reads (or creates, mode 0600) the token file.
func LoadOrCreateToken(path string) (*Token, string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		buf := make([]byte, 32)
		if _, err := rand.Read(buf); err != nil {
			return nil, "", err
		}
		tok := "sbx_" + hex.EncodeToString(buf)
		if err := os.WriteFile(path, []byte(tok+"\n"), 0o600); err != nil {
			return nil, "", err
		}
		b = []byte(tok)
	}
	tok := strings.TrimSpace(string(b))
	return &Token{hash: sha256.Sum256([]byte(tok))}, tok, nil
}

func (t *Token) ok(presented string) bool {
	h := sha256.Sum256([]byte(presented))
	return subtle.ConstantTimeCompare(h[:], t.hash[:]) == 1
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": code, "message": msg, "retryable": false}})
}

func allowedHost(h string) bool {
	if h == "" {
		return true
	}
	host := h
	if hh, _, err := net.SplitHostPort(h); err == nil {
		host = hh
	}
	host = strings.Trim(host, "[]")
	switch host {
	case "localhost", "127.0.0.1", "::1", "sandboxd", "unix":
		return true
	}
	return false
}

// Auth authenticates the request and rejects browser-originated traffic.
func Auth(tok *Token, daemonUID int, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Origin") != "" || r.Header.Get("Sec-Fetch-Site") != "" {
			writeErr(w, http.StatusForbidden, "unauthorized", "browser requests are not allowed")
			return
		}
		if !allowedHost(r.Host) {
			writeErr(w, http.StatusForbidden, "unauthorized", "Host must be localhost")
			return
		}
		peer, _ := PeerFrom(r.Context())
		uid := -1
		switch peer.Transport {
		case "unix":
			if peer.UID != daemonUID && peer.UID != 0 && daemonUID != 0 {
				writeErr(w, http.StatusUnauthorized, "unauthorized", "socket peer uid not allowed")
				return
			}
			uid = peer.UID
			// A bearer token is also accepted (e.g. forwarded sockets).
		default:
			auth := r.Header.Get("Authorization")
			t, ok := strings.CutPrefix(auth, "Bearer ")
			if !ok || tok == nil || !tok.ok(strings.TrimSpace(t)) {
				writeErr(w, http.StatusUnauthorized, "unauthorized", "missing or invalid bearer token")
				return
			}
		}
		name := r.Header.Get("X-Sandboxd-Client")
		if name == "" {
			name = "default"
		}
		if len(name) > 64 {
			name = name[:64]
		}
		prefix := "token"
		if uid >= 0 {
			prefix = "uid" + itoa(uid)
		}
		ctx := context.WithValue(r.Context(), keyCaller, Caller{Name: prefix + ":" + name, UID: uid})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func itoa(i int) string {
	b, _ := json.Marshal(i)
	return string(b)
}

// Common adds request ids, version headers, panic recovery, and logging.
func Common(log *slog.Logger, version, minClient string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 8)
		_, _ = rand.Read(buf)
		id := "req_" + hex.EncodeToString(buf)
		w.Header().Set("X-Request-Id", id)
		w.Header().Set("X-Sandboxd-Version", version)
		w.Header().Set("X-Sandboxd-Min-Client", minClient)
		ctx := context.WithValue(r.Context(), keyRequestID, id)
		sw := &statusWriter{ResponseWriter: w, status: 200}
		t0 := time.Now()
		defer func() {
			if rec := recover(); rec != nil {
				log.Error("panic in handler", "path", r.URL.Path, "panic", rec)
				if !sw.wrote {
					writeErr(sw, http.StatusInternalServerError, "internal", "internal error")
				}
			}
			log.Debug("http", "method", r.Method, "path", r.URL.Path, "status", sw.status, "ms", time.Since(t0).Milliseconds(), "req", id)
		}()
		next.ServeHTTP(sw, r.WithContext(ctx))
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (s *statusWriter) WriteHeader(code int) {
	s.status = code
	s.wrote = true
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusWriter) Write(b []byte) (int, error) {
	s.wrote = true
	return s.ResponseWriter.Write(b)
}

func (s *statusWriter) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (s *statusWriter) Unwrap() http.ResponseWriter { return s.ResponseWriter }
