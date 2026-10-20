// Package client is the Go SDK for the sandboxd HTTP API.
//
//	c, _ := client.New(client.Options{})
//	sb, _ := c.CreateSandbox(ctx, client.CreateSandboxRequest{Workspace: &client.WorkspaceSpec{HostPath: "."}})
//	res, _ := c.Exec(ctx, sb.ID, client.ExecRequest{Cmd: "npm test"})
//	fmt.Println(*res.ExitCode, res.Stdout)
package client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Version of this client library.
const Version = "0.1.0"

// Options configure a Client. Zero values use the local daemon socket.
type Options struct {
	Socket  string // unix socket path (default: SANDBOXD_SOCKET or the per-OS default)
	URL     string // e.g. http://127.0.0.1:7070 (uses Token)
	Token   string // bearer token (default: SANDBOXD_TOKEN)
	Name    string // client name for fairness and logs, e.g. "my-harness"
	Timeout time.Duration
}

// Client talks to sandboxd.
type Client struct {
	base string
	http *http.Client
	tok  string
	name string
}

// Error is a structured API error.
type Error struct {
	Status int
	ErrorDetail
}

func (e *Error) Error() string {
	if e.RetryAfterS > 0 {
		return fmt.Sprintf("%s: %s (retry in %ds)", e.Code, e.Message, e.RetryAfterS)
	}
	return e.Code + ": " + e.Message
}

// IsCode reports whether err is an API error with the given code.
func IsCode(err error, code string) bool {
	var e *Error
	return errors.As(err, &e) && e.Code == code
}

// DefaultSocket mirrors the daemon's default socket location.
func DefaultSocket() string {
	if s := os.Getenv("SANDBOXD_SOCKET"); s != "" {
		return s
	}
	root := os.Getenv("SANDBOXD_ROOT")
	if root == "" {
		home, _ := os.UserHomeDir()
		switch {
		case runtime.GOOS == "darwin":
			root = filepath.Join(home, "Library", "Application Support", "sandboxd")
		case os.Geteuid() == 0 || fileExists("/run/sandboxd/sandboxd.sock"):
			return "/run/sandboxd/sandboxd.sock"
		default:
			root = filepath.Join(home, ".local", "share", "sandboxd")
		}
	}
	p := filepath.Join(root, "sandboxd.sock")
	if len(p) < 100 {
		return p
	}
	return filepath.Join(os.TempDir(), "sandboxd-"+strconv.Itoa(os.Getuid()), "sandboxd.sock")
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

// New creates a client.
func New(o Options) (*Client, error) {
	if o.URL == "" {
		o.URL = os.Getenv("SANDBOXD_URL")
	}
	if o.Token == "" {
		o.Token = os.Getenv("SANDBOXD_TOKEN")
	}
	if o.Name == "" {
		o.Name = "go-client"
	}
	tr := &http.Transport{MaxIdleConns: 8, IdleConnTimeout: 60 * time.Second}
	c := &Client{tok: o.Token, name: o.Name}
	if o.URL != "" {
		u, err := url.Parse(o.URL)
		if err != nil {
			return nil, err
		}
		c.base = strings.TrimRight(u.String(), "/")
	} else {
		sock := o.Socket
		if sock == "" {
			sock = DefaultSocket()
		}
		tr.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		}
		c.base = "http://sandboxd"
	}
	c.http = &http.Client{Transport: tr, Timeout: o.Timeout}
	return c, nil
}

func (c *Client) req(ctx context.Context, method, path string, body any, out any) error {
	var rdr io.Reader
	ct := ""
	switch b := body.(type) {
	case nil:
	case io.Reader:
		rdr = b
		ct = "application/octet-stream"
	default:
		j, err := json.Marshal(b)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(j)
		ct = "application/json"
	}
	return c.do(ctx, method, path, rdr, ct, nil, out)
}

func (c *Client) do(ctx context.Context, method, path string, body io.Reader, ct string, hdr map[string]string, out any) error {
	r, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return err
	}
	if ct != "" {
		r.Header.Set("Content-Type", ct)
	}
	if c.tok != "" {
		r.Header.Set("Authorization", "Bearer "+c.tok)
	}
	r.Header.Set("X-Sandboxd-Client", c.name)
	r.Header.Set("X-Sandboxd-Client-Version", Version)
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	resp, err := c.http.Do(r)
	if err != nil {
		return fmt.Errorf("sandboxd unreachable (%v); is the daemon running? try `sandboxctl doctor`", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		var eb ErrorBody
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if json.Unmarshal(b, &eb) == nil && eb.Error.Code != "" {
			return &Error{Status: resp.StatusCode, ErrorDetail: eb.Error}
		}
		return &Error{Status: resp.StatusCode, ErrorDetail: ErrorDetail{Code: http.StatusText(resp.StatusCode), Message: strings.TrimSpace(string(b))}}
	}
	switch o := out.(type) {
	case nil:
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	case *[]byte:
		b, err := io.ReadAll(resp.Body)
		*o = b
		return err
	default:
		return json.NewDecoder(resp.Body).Decode(out)
	}
}

// Health checks the daemon.
func (c *Client) Health(ctx context.Context) (Health, error) {
	var h Health
	return h, c.req(ctx, "GET", "/v1/health", nil, &h)
}

// Capacity returns free budget.
func (c *Client) Capacity(ctx context.Context) (Capacity, error) {
	var v Capacity
	return v, c.req(ctx, "GET", "/v1/capacity", nil, &v)
}

// CreateSandbox creates a sandbox (or reuses one with the same session key).
func (c *Client) CreateSandbox(ctx context.Context, r CreateSandboxRequest) (Sandbox, error) {
	var v Sandbox
	return v, c.req(ctx, "POST", "/v1/sandboxes", r, &v)
}

// GetSandbox fetches a sandbox.
func (c *Client) GetSandbox(ctx context.Context, id string) (Sandbox, error) {
	var v Sandbox
	return v, c.req(ctx, "GET", "/v1/sandboxes/"+id, nil, &v)
}

// ListSandboxes lists sandboxes; query may set state, session_key, all.
func (c *Client) ListSandboxes(ctx context.Context, query url.Values) ([]Sandbox, error) {
	var v struct {
		Sandboxes []Sandbox `json:"sandboxes"`
	}
	p := "/v1/sandboxes"
	if len(query) > 0 {
		p += "?" + query.Encode()
	}
	return v.Sandboxes, c.req(ctx, "GET", p, nil, &v)
}

// DestroySandbox deletes a sandbox.
func (c *Client) DestroySandbox(ctx context.Context, id string) error {
	return c.req(ctx, "DELETE", "/v1/sandboxes/"+id, nil, nil)
}

// Pause pauses a sandbox to disk; Resume brings it back.
func (c *Client) Pause(ctx context.Context, id string) (Sandbox, error) {
	var v Sandbox
	return v, c.req(ctx, "POST", "/v1/sandboxes/"+id+"/pause", nil, &v)
}

func (c *Client) Resume(ctx context.Context, id string) (Sandbox, error) {
	var v Sandbox
	return v, c.req(ctx, "POST", "/v1/sandboxes/"+id+"/resume", nil, &v)
}

func (c *Client) Keepalive(ctx context.Context, id string) (Sandbox, error) {
	var v Sandbox
	return v, c.req(ctx, "POST", "/v1/sandboxes/"+id+"/keepalive", nil, &v)
}

// Exec runs a command. idempotencyKey may be empty.
func (c *Client) Exec(ctx context.Context, id string, r ExecRequest, idempotencyKey ...string) (Execution, error) {
	var v Execution
	j, _ := json.Marshal(r)
	hdr := map[string]string{}
	if len(idempotencyKey) > 0 && idempotencyKey[0] != "" {
		hdr["Idempotency-Key"] = idempotencyKey[0]
	}
	return v, c.do(ctx, "POST", "/v1/sandboxes/"+id+"/exec", bytes.NewReader(j), "application/json", hdr, &v)
}

// GetExecution fetches an execution (poll this for wait=false runs).
func (c *Client) GetExecution(ctx context.Context, id string) (Execution, error) {
	var v Execution
	return v, c.req(ctx, "GET", "/v1/executions/"+id, nil, &v)
}

// ListExecutions lists recent executions in a sandbox.
func (c *Client) ListExecutions(ctx context.Context, sandboxID string) ([]Execution, error) {
	var v struct {
		Executions []Execution `json:"executions"`
	}
	return v.Executions, c.req(ctx, "GET", "/v1/sandboxes/"+sandboxID+"/executions", nil, &v)
}

// Output returns a page of raw output.
func (c *Client) Output(ctx context.Context, execID, stream string, offset, limit int64) (OutputChunk, error) {
	var v OutputChunk
	q := url.Values{"stream": {stream}, "offset": {strconv.FormatInt(offset, 10)}, "limit": {strconv.FormatInt(limit, 10)}}
	return v, c.req(ctx, "GET", "/v1/executions/"+execID+"/output?"+q.Encode(), nil, &v)
}

// Grep searches an execution's output.
func (c *Client) Grep(ctx context.Context, execID, pattern, stream string) (GrepResult, error) {
	var v GrepResult
	q := url.Values{"pattern": {pattern}, "stream": {stream}}
	return v, c.req(ctx, "GET", "/v1/executions/"+execID+"/grep?"+q.Encode(), nil, &v)
}

// Signal signals a running execution (default SIGTERM).
func (c *Client) Signal(ctx context.Context, execID string, sig int) error {
	return c.req(ctx, "POST", "/v1/executions/"+execID+"/signal", map[string]int{"signal": sig}, nil)
}

// WriteStdin writes to a running execution's stdin (close ends input).
func (c *Client) WriteStdin(ctx context.Context, execID string, data []byte, closeIt ...bool) error {
	body := map[string]any{"data": string(data), "close": len(closeIt) > 0 && closeIt[0]}
	return c.req(ctx, "POST", "/v1/executions/"+execID+"/stdin", body, nil)
}

// StreamFrame is one SSE event from an execution stream.
type StreamFrame struct {
	Seq    int64
	Stream string // stdout | stderr | exit
	Data   string
	Exit   *Execution
}

// Stream follows an execution's output from after (0 = start) and calls fn
// for each frame until the execution exits or ctx ends.
func (c *Client) Stream(ctx context.Context, execID string, after int64, fn func(StreamFrame) error) error {
	r, err := http.NewRequestWithContext(ctx, "GET", c.base+"/v1/executions/"+execID+"/stream", nil)
	if err != nil {
		return err
	}
	if c.tok != "" {
		r.Header.Set("Authorization", "Bearer "+c.tok)
	}
	r.Header.Set("X-Sandboxd-Client", c.name)
	if after > 0 {
		r.Header.Set("Last-Event-ID", strconv.FormatInt(after, 10))
	}
	hc := *c.http
	hc.Timeout = 0
	resp, err := hc.Do(r)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("stream: %s", strings.TrimSpace(string(b)))
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), 8<<20)
	var f StreamFrame
	var data []string
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			if f.Stream != "" {
				payload := strings.Join(data, "\n")
				if f.Stream == "exit" {
					var e Execution
					_ = json.Unmarshal([]byte(payload), &e)
					f.Exit = &e
				} else {
					var d struct {
						Data string `json:"data"`
					}
					_ = json.Unmarshal([]byte(payload), &d)
					f.Data = d.Data
				}
				if err := fn(f); err != nil {
					return err
				}
				if f.Stream == "exit" {
					return nil
				}
			}
			f, data = StreamFrame{}, nil
		case strings.HasPrefix(line, "id: "):
			f.Seq, _ = strconv.ParseInt(line[4:], 10, 64)
		case strings.HasPrefix(line, "event: "):
			f.Stream = line[7:]
		case strings.HasPrefix(line, "data: "):
			data = append(data, line[6:])
		}
	}
	return sc.Err()
}

// WriteFile uploads data to path inside the sandbox.
func (c *Client) WriteFile(ctx context.Context, id, path string, data []byte) error {
	return c.do(ctx, "PUT", "/v1/sandboxes/"+id+"/files?path="+url.QueryEscape(path), bytes.NewReader(data), "application/octet-stream", nil, nil)
}

// ReadFile downloads a file from the sandbox.
func (c *Client) ReadFile(ctx context.Context, id, path string) ([]byte, error) {
	var b []byte
	return b, c.do(ctx, "GET", "/v1/sandboxes/"+id+"/files?path="+url.QueryEscape(path), nil, "", nil, &b)
}

// Sync pushes host workspace changes into the sandbox.
func (c *Client) Sync(ctx context.Context, id string) (SyncResult, error) {
	var v SyncResult
	return v, c.req(ctx, "POST", "/v1/sandboxes/"+id+"/sync", nil, &v)
}

// Diff returns the sandbox's workspace changes.
func (c *Client) Diff(ctx context.Context, id string) (DiffResult, error) {
	var v DiffResult
	return v, c.req(ctx, "GET", "/v1/sandboxes/"+id+"/diff", nil, &v)
}

// Apply writes sandbox changes to the host repo (with a backup).
func (c *Client) Apply(ctx context.Context, id string, r ApplyRequest) (ApplyResult, error) {
	var v ApplyResult
	return v, c.req(ctx, "POST", "/v1/sandboxes/"+id+"/apply", r, &v)
}

// Pull copies changed files from the sandbox to the host (with a backup).
func (c *Client) Pull(ctx context.Context, id string, r PullRequest) (ApplyResult, error) {
	var v ApplyResult
	return v, c.req(ctx, "POST", "/v1/sandboxes/"+id+"/pull", r, &v)
}

// Undo restores a backup artifact.
func (c *Client) Undo(ctx context.Context, artifactID string) (UndoResult, error) {
	var v UndoResult
	return v, c.req(ctx, "POST", "/v1/artifacts/"+artifactID+"/undo", nil, &v)
}

// Checkpoint snapshots a running sandbox.
func (c *Client) Checkpoint(ctx context.Context, id, label string) (Snapshot, error) {
	var v Snapshot
	return v, c.req(ctx, "POST", "/v1/sandboxes/"+id+"/snapshots", SnapshotRequest{Label: label}, &v)
}

// Fork clones a sandbox count times.
func (c *Client) Fork(ctx context.Context, id string, count int) (ForkResult, error) {
	var v ForkResult
	return v, c.req(ctx, "POST", "/v1/sandboxes/"+id+"/fork", ForkRequest{Count: count}, &v)
}

// Restore creates a new sandbox from a snapshot ("latest" by default).
func (c *Client) Restore(ctx context.Context, id, snapshotID string) (Sandbox, error) {
	var v Sandbox
	return v, c.req(ctx, "POST", "/v1/sandboxes/"+id+"/restore", RestoreRequest{SnapshotID: snapshotID}, &v)
}

// ListSnapshots lists snapshots (optionally for one sandbox).
func (c *Client) ListSnapshots(ctx context.Context, sandboxID string) ([]Snapshot, error) {
	var v struct {
		Snapshots []Snapshot `json:"snapshots"`
	}
	p := "/v1/snapshots"
	if sandboxID != "" {
		p += "?sandbox_id=" + url.QueryEscape(sandboxID)
	}
	return v.Snapshots, c.req(ctx, "GET", p, nil, &v)
}

// ForwardPort exposes a guest port on 127.0.0.1.
func (c *Client) ForwardPort(ctx context.Context, id string, guestPort int) (PortResult, error) {
	var v PortResult
	return v, c.req(ctx, "POST", "/v1/sandboxes/"+id+"/ports", PortRequest{GuestPort: guestPort}, &v)
}

// Recover extracts a lost sandbox's workspace into an artifact.
func (c *Client) Recover(ctx context.Context, id string) (Artifact, error) {
	var v Artifact
	return v, c.req(ctx, "POST", "/v1/sandboxes/"+id+"/recover", nil, &v)
}

// ListArtifacts lists artifacts.
func (c *Client) ListArtifacts(ctx context.Context, sandboxID, kind string) ([]Artifact, error) {
	var v struct {
		Artifacts []Artifact `json:"artifacts"`
	}
	q := url.Values{}
	if sandboxID != "" {
		q.Set("sandbox_id", sandboxID)
	}
	if kind != "" {
		q.Set("kind", kind)
	}
	return v.Artifacts, c.req(ctx, "GET", "/v1/artifacts?"+q.Encode(), nil, &v)
}

// ArtifactContent downloads an artifact's bytes.
func (c *Client) ArtifactContent(ctx context.Context, id string) ([]byte, error) {
	var b []byte
	return b, c.do(ctx, "GET", "/v1/artifacts/"+id+"/content", nil, "", nil, &b)
}

// Templates lists templates; CreateTemplate builds one.
func (c *Client) Templates(ctx context.Context) ([]Template, error) {
	var v struct {
		Templates []Template `json:"templates"`
	}
	return v.Templates, c.req(ctx, "GET", "/v1/templates", nil, &v)
}

func (c *Client) CreateTemplate(ctx context.Context, r TemplateRequest) (Template, error) {
	var v Template
	return v, c.req(ctx, "POST", "/v1/templates", r, &v)
}

// GC runs garbage collection.
func (c *Client) GC(ctx context.Context, dryRun bool) (GCResult, error) {
	var v GCResult
	return v, c.req(ctx, "POST", "/v1/gc", GCRequest{DryRun: dryRun}, &v)
}

// Events follows the daemon event feed.
func (c *Client) Events(ctx context.Context, sandboxID string, after int64, fn func(Event) error) error {
	p := "/v1/events"
	if sandboxID != "" {
		p += "?sandbox_id=" + url.QueryEscape(sandboxID)
	}
	r, err := http.NewRequestWithContext(ctx, "GET", c.base+p, nil)
	if err != nil {
		return err
	}
	if c.tok != "" {
		r.Header.Set("Authorization", "Bearer "+c.tok)
	}
	r.Header.Set("X-Sandboxd-Client", c.name)
	if after > 0 {
		r.Header.Set("Last-Event-ID", strconv.FormatInt(after, 10))
	}
	hc := *c.http
	hc.Timeout = 0
	resp, err := hc.Do(r)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), 8<<20)
	for sc.Scan() {
		line := sc.Text()
		if d, ok := strings.CutPrefix(line, "data: "); ok {
			var e Event
			if json.Unmarshal([]byte(d), &e) == nil {
				if err := fn(e); err != nil {
					return err
				}
			}
		}
	}
	return sc.Err()
}

// Bool returns a pointer, for optional request fields.
func Bool(b bool) *bool { return &b }
