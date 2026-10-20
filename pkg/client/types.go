package client

import "encoding/json"

// WorkspaceSpec links a sandbox to a host repo.
type WorkspaceSpec struct {
	HostPath string `json:"host_path,omitempty"`
	// Mode: "host" (host repo is the source of truth, auto-sync before every
	// exec), "sandbox" (VM is the source of truth; use diff/apply), "none".
	Mode string `json:"mode,omitempty"`
}

// NetworkSpec sets egress rules.
type NetworkSpec struct {
	Mode  string   `json:"mode,omitempty"` // none | allowlist | full
	Allow []string `json:"allow,omitempty"`
	Deny  []string `json:"deny,omitempty"`
	Ports []uint16 `json:"ports,omitempty"`
}

// CreateSandboxRequest is the body of POST /v1/sandboxes.
type CreateSandboxRequest struct {
	Template         string            `json:"template,omitempty"` // "base" (default) | "auto" | template name/id
	VCPU             int               `json:"vcpu,omitempty"`
	MemMB            int               `json:"mem_mb,omitempty"`
	DiskMB           int               `json:"disk_mb,omitempty"`
	Workspace        *WorkspaceSpec    `json:"workspace,omitempty"`
	Network          *NetworkSpec      `json:"network,omitempty"`
	IdleTimeoutS     int               `json:"idle_timeout_s,omitempty"`
	MaxLifetimeS     int               `json:"max_lifetime_s,omitempty"`
	SessionKey       string            `json:"session_key,omitempty"`
	WaitForCapacityS int               `json:"wait_for_capacity_s,omitempty"`
	Metadata         map[string]string `json:"metadata,omitempty"`
}

// Sandbox is the public view of a sandbox.
type Sandbox struct {
	ID            string            `json:"id"`
	State         string            `json:"state"`
	Health        string            `json:"health"`
	StateReason   string            `json:"state_reason,omitempty"`
	TemplateID    string            `json:"template_id"`
	TemplateName  string            `json:"template_name,omitempty"`
	SessionKey    string            `json:"session_key,omitempty"`
	VCPU          int               `json:"vcpu"`
	MemMB         int               `json:"mem_mb"`
	DiskMB        int               `json:"disk_mb"`
	NetworkMode   string            `json:"network_mode"`
	WorkspaceMode string            `json:"workspace_mode"`
	HostPath      string            `json:"host_path,omitempty"`
	BootedFrom    string            `json:"booted_from,omitempty"` // pool | snapshot | cold | resume
	BootMs        int64             `json:"boot_ms,omitempty"`
	ExecCount     int               `json:"exec_count"`
	Ports         map[string]int    `json:"ports,omitempty"`
	CreatedAt     int64             `json:"created_at"`
	ReadyAt       int64             `json:"ready_at,omitempty"`
	LastActiveAt  int64             `json:"last_active_at"`
	ExpiresAt     int64             `json:"expires_at"`
	Metadata      map[string]string `json:"metadata,omitempty"`
	Reused        bool              `json:"reused,omitempty"` // returned an existing session sandbox
	Notice        string            `json:"notice,omitempty"`
}

// ExecRequest is the body of POST /v1/sandboxes/{id}/exec.
type ExecRequest struct {
	Cmd            string            `json:"cmd,omitempty"`
	Argv           []string          `json:"argv,omitempty"`
	Cwd            string            `json:"cwd,omitempty"`
	Env            map[string]string `json:"env,omitempty"`
	User           string            `json:"user,omitempty"`
	TimeoutS       int               `json:"timeout_s,omitempty"`
	Wait           *bool             `json:"wait,omitempty"` // default true
	Background     bool              `json:"background,omitempty"`
	TTY            bool              `json:"tty,omitempty"`
	Stdin          string            `json:"stdin,omitempty"`
	KeepStdinOpen  bool              `json:"keep_stdin_open,omitempty"`
	StripANSI      *bool             `json:"strip_ansi,omitempty"` // default true
	MaxOutputBytes int               `json:"max_output_bytes,omitempty"`
	Sync           *bool             `json:"sync,omitempty"` // default true in host workspace mode
}

// SyncResult reports a workspace delta sync.
type SyncResult struct {
	Sent       int      `json:"sent"`
	Deleted    int      `json:"deleted"`
	Bytes      int64    `json:"bytes"`
	Skipped    []string `json:"skipped,omitempty"`
	DurationMs int64    `json:"duration_ms"`
}

// Execution is the public view of an execution.
type Execution struct {
	ID              string            `json:"exec_id"`
	SandboxID       string            `json:"sandbox_id"`
	Status          string            `json:"status"` // running | succeeded | failed | timed_out | killed | lost
	Command         string            `json:"command"`
	ExitCode        *int              `json:"exit_code"`
	Signal          int               `json:"signal,omitempty"`
	TimedOut        bool              `json:"timed_out"`
	OOMKilled       bool              `json:"oom_killed"`
	DurationMs      int64             `json:"duration_ms"`
	Stdout          string            `json:"stdout"`
	Stderr          string            `json:"stderr"`
	StdoutTruncated bool              `json:"stdout_truncated"`
	StderrTruncated bool              `json:"stderr_truncated"`
	StdoutBytes     int64             `json:"stdout_bytes"`
	StderrBytes     int64             `json:"stderr_bytes"`
	ChangedFiles    []string          `json:"changed_files"`
	Reason          string            `json:"reason,omitempty"`
	StartedAt       int64             `json:"started_at"`
	FinishedAt      int64             `json:"finished_at,omitempty"`
	LogURLs         map[string]string `json:"log_urls,omitempty"`
	Sync            *SyncResult       `json:"sync,omitempty"`
}

// OutputChunk is a page of raw output.
type OutputChunk struct {
	Stream    string `json:"stream"`
	Offset    int64  `json:"offset"`
	Data      string `json:"data"`
	TotalSize int64  `json:"total_size"`
	EOF       bool   `json:"eof"`
}

// GrepResult lists matching lines.
type GrepResult struct {
	Stream    string      `json:"stream"`
	Matches   []GrepMatch `json:"matches"`
	Truncated bool        `json:"truncated"`
}

type GrepMatch struct {
	Line int    `json:"line"`
	Text string `json:"text"`
}

// Change is one changed path in the sandbox.
type Change struct {
	Path    string `json:"path"`
	Status  string `json:"status"`
	OldPath string `json:"old_path,omitempty"`
}

// DiffResult is the sandbox's workspace diff against its git HEAD.
type DiffResult struct {
	ArtifactID string   `json:"artifact_id"`
	Head       string   `json:"head"`
	Changes    []Change `json:"changes"`
	Patch      string   `json:"patch"`
	PatchBytes int      `json:"patch_bytes"`
	Truncated  bool     `json:"truncated"`
}

// ApplyRequest applies a diff artifact (default: a fresh diff) to the host.
type ApplyRequest struct {
	ArtifactID string   `json:"artifact_id,omitempty"`
	Paths      []string `json:"paths,omitempty"`
}

// PullRequest copies sandbox files to the host.
type PullRequest struct {
	Paths []string `json:"paths,omitempty"` // default: changed files of the last exec
	Force bool     `json:"force,omitempty"`
}

// FileResult is the outcome for one path in apply/pull.
type FileResult struct {
	Path   string `json:"path"`
	Action string `json:"action"`
	Reason string `json:"reason,omitempty"`
}

// ApplyResult reports host writes and the backup that can undo them.
type ApplyResult struct {
	BackupArtifactID string       `json:"backup_artifact_id,omitempty"`
	Files            []FileResult `json:"files"`
	Applied          int          `json:"applied"`
	Conflicts        int          `json:"conflicts"`
	Skipped          int          `json:"skipped"`
}

// UndoResult reports a restored backup.
type UndoResult struct {
	Restored int `json:"restored"`
	Removed  int `json:"removed"`
}

// Snapshot is the public view of a snapshot.
type Snapshot struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	SandboxID  string `json:"sandbox_id,omitempty"`
	TemplateID string `json:"template_id,omitempty"`
	Label      string `json:"label,omitempty"`
	CreatedAt  int64  `json:"created_at"`
	SizeBytes  int64  `json:"size_bytes"`
}

// SnapshotRequest creates a checkpoint.
type SnapshotRequest struct {
	Label string `json:"label,omitempty"`
}

// ForkRequest clones a sandbox N times.
type ForkRequest struct {
	Count int `json:"count"`
}

// ForkResult lists the clones.
type ForkResult struct {
	SnapshotID string    `json:"snapshot_id"`
	Sandboxes  []Sandbox `json:"sandboxes"`
	Failed     []string  `json:"failed,omitempty"` // clones that could not start (e.g. capacity)
}

// RestoreRequest creates a new sandbox from a snapshot of this sandbox.
type RestoreRequest struct {
	SnapshotID string `json:"snapshot_id,omitempty"` // default "latest"
}

// PortRequest exposes a guest port on 127.0.0.1.
type PortRequest struct {
	GuestPort int `json:"guest_port"`
	HostPort  int `json:"host_port,omitempty"`
}

// PortResult is the host side of a forward.
type PortResult struct {
	GuestPort int    `json:"guest_port"`
	HostPort  int    `json:"host_port"`
	URL       string `json:"url"`
}

// Artifact is a stored diff, backup, or recovery bundle.
type Artifact struct {
	ID          string          `json:"id"`
	SandboxID   string          `json:"sandbox_id,omitempty"`
	ExecutionID string          `json:"execution_id,omitempty"`
	Kind        string          `json:"kind"`
	SizeBytes   int64           `json:"size_bytes"`
	CreatedAt   int64           `json:"created_at"`
	ExpiresAt   int64           `json:"expires_at,omitempty"`
	Meta        json.RawMessage `json:"meta,omitempty"`
}

// Template is the public view of a template.
type Template struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Status       string `json:"status"`
	StatusReason string `json:"status_reason,omitempty"`
	SpecHash     string `json:"spec_hash"`
	DetectedFrom string `json:"detected_from,omitempty"`
	RepoPath     string `json:"repo_path,omitempty"`
	GoldenID     string `json:"golden_snapshot_id,omitempty"`
	CreatedAt    int64  `json:"created_at"`
	BuiltAt      int64  `json:"built_at,omitempty"`
}

// TemplateRequest builds a template.
type TemplateRequest struct {
	Name       string   `json:"name,omitempty"`
	RepoPath   string   `json:"repo_path,omitempty"` // auto-detect from a repo
	Base       string   `json:"base,omitempty"`      // base template (default "base")
	Install    []string `json:"install,omitempty"`
	ReadyCheck string   `json:"ready_check,omitempty"`
	Wait       bool     `json:"wait,omitempty"`
}

// Health is GET /v1/health.
type Health struct {
	Status          string `json:"status"`
	Version         string `json:"version"`
	Backend         string `json:"backend"`
	VMMVersion      string `json:"vmm_version"`
	AgentVersion    string `json:"agent_version"`
	KernelVersion   string `json:"kernel_version"`
	MinClient       string `json:"min_client_version"`
	BaseTemplate    string `json:"base_template_status"`
	UptimeS         int64  `json:"uptime_s"`
	DataRoot        string `json:"data_root"`
}

// Capacity is GET /v1/capacity.
type Capacity struct {
	VCPUTotal  float64        `json:"vcpu_total"`
	VCPUFree   float64        `json:"vcpu_free"`
	MemTotalMB int            `json:"mem_total_mb"`
	MemFreeMB  int            `json:"mem_free_mb"`
	Sandboxes  int            `json:"sandboxes"`
	MaxSandbox int            `json:"max_sandboxes"`
	PoolVMs    int            `json:"pool_vms"`
	QueueLen   int            `json:"queue_len"`
	Pressure   string         `json:"memory_pressure"`
	Warm       map[string]int `json:"warm"`
}

// Event is one daemon event.
type Event struct {
	ID          int64           `json:"id"`
	Ts          int64           `json:"ts"`
	SandboxID   string          `json:"sandbox_id,omitempty"`
	ExecutionID string          `json:"execution_id,omitempty"`
	Type        string          `json:"type"`
	Data        json.RawMessage `json:"data,omitempty"`
}

// GCRequest runs garbage collection.
type GCRequest struct {
	DryRun bool `json:"dry_run"`
}

// GCResult lists what was (or would be) deleted.
type GCResult struct {
	DryRun  bool     `json:"dry_run"`
	Deleted []string `json:"deleted"`
	Bytes   int64    `json:"bytes"`
}

// ErrorBody is the JSON error envelope.
type ErrorBody struct {
	Error ErrorDetail `json:"error"`
}

// ErrorDetail describes an API error.
type ErrorDetail struct {
	Code        string `json:"code"`
	Message     string `json:"message"`
	Retryable   bool   `json:"retryable"`
	RetryAfterS int    `json:"retry_after_s,omitempty"`
}
