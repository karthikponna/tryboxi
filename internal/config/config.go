// Package config holds every tunable default (budgets, timeouts, warm pool,
// network allowlist, sync excludes, output limits, retention) and loads
// overrides from config.yaml.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"

	"gopkg.in/yaml.v3"

	"github.com/karthikponna/tryboxi/internal/network/policy"
)

type Budgets struct {
	MaxVCPU        int     `yaml:"max_vcpu"`
	VCPUOvercommit float64 `yaml:"vcpu_overcommit"`
	MaxMemMB       int     `yaml:"max_mem_mb"`
	MaxSandboxes   int     `yaml:"max_sandboxes"`
	MaxDiskGB      int     `yaml:"max_disk_gb"`
	MaxSnapshotGB  int     `yaml:"max_snapshot_gb"`
}

type Defaults struct {
	VCPU            int `yaml:"vcpu"`
	MemMB           int `yaml:"mem_mb"`
	DiskMB          int `yaml:"disk_mb"`
	IdleTimeoutS    int `yaml:"idle_timeout_s"`
	MaxLifetimeS    int `yaml:"max_lifetime_s"`
	PausedTTLS      int `yaml:"paused_ttl_s"`
	ExecTimeoutS    int `yaml:"exec_timeout_s"`
	MaxExecTimeoutS int `yaml:"max_exec_timeout_s"`
	StallWarnS      int `yaml:"stall_warn_s"`
}

type Queue struct {
	MaxLen           int `yaml:"max_len"`
	WaitForCapacityS int `yaml:"wait_for_capacity_s"`
}

type WarmPool struct {
	Enabled     bool `yaml:"enabled"`
	MinWarm     int  `yaml:"min_warm"`
	MaxWarm     int  `yaml:"max_warm"`
	IdleShrinkS int  `yaml:"idle_shrink_s"`
}

type Network struct {
	DefaultMode policy.Mode `yaml:"default_mode"`
	Allow       []string    `yaml:"allow"`
	Deny        []string    `yaml:"deny"`
}

type Sync struct {
	Excludes       []string `yaml:"excludes"`
	SecretExcludes []string `yaml:"secret_excludes"`
	MaxFileMB      int      `yaml:"max_file_mb"`
}

type Output struct {
	HeadLines      int   `yaml:"head_lines"`
	HeadBytes      int   `yaml:"head_bytes"`
	TailLines      int   `yaml:"tail_lines"`
	TailBytes      int   `yaml:"tail_bytes"`
	MaxStoredBytes int64 `yaml:"max_stored_bytes"`
	RingBytes      int   `yaml:"ring_bytes"`
}

type Retention struct {
	EventsDays        int `yaml:"events_days"`
	ExecLogsDays      int `yaml:"exec_logs_days"`
	DestroyedRowsDays int `yaml:"destroyed_rows_days"`
	ArtifactsDays     int `yaml:"artifacts_days"`
	RecoveryHours     int `yaml:"recovery_hours"`
	SnapshotHours     int `yaml:"snapshot_hours"`
}

type Health struct {
	HeartbeatMS     int `yaml:"heartbeat_ms"`
	UnhealthyAfterS int `yaml:"unhealthy_after_s"`
	LostAfterS      int `yaml:"lost_after_s"`
	DeadmanS        int `yaml:"deadman_s"`
}

// Config is the full daemon configuration.
type Config struct {
	DataRoot  string    `yaml:"data_root"`
	Socket    string    `yaml:"socket"`
	TCPListen string    `yaml:"tcp_listen"` // e.g. 127.0.0.1:7070 (bearer token auth)
	LogLevel  string    `yaml:"log_level"`
	Budgets   Budgets   `yaml:"budgets"`
	Defaults  Defaults  `yaml:"defaults"`
	Queue     Queue     `yaml:"queue"`
	WarmPool  WarmPool  `yaml:"warm_pool"`
	Network   Network   `yaml:"network"`
	Sync      Sync      `yaml:"sync"`
	Output    Output    `yaml:"output"`
	Retention Retention `yaml:"retention"`
	Health    Health    `yaml:"health"`
}

// DefaultDataRoot returns the per-OS data root, overridable by SANDBOXD_ROOT.
func DefaultDataRoot() string {
	if r := os.Getenv("SANDBOXD_ROOT"); r != "" {
		return r
	}
	if runtime.GOOS == "darwin" {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, "Library", "Application Support", "sandboxd")
	}
	if os.Geteuid() == 0 {
		return "/var/lib/sandboxd"
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "sandboxd")
}

// DefaultSocket picks a socket path that fits the 104-byte sun_path limit.
func DefaultSocket(dataRoot string) string {
	if s := os.Getenv("SANDBOXD_SOCKET"); s != "" {
		return s
	}
	if runtime.GOOS == "linux" && os.Geteuid() == 0 {
		return "/run/sandboxd/sandboxd.sock"
	}
	p := filepath.Join(dataRoot, "sandboxd.sock")
	if len(p) < 100 {
		return p
	}
	return filepath.Join(os.TempDir(), "sandboxd-"+strconv.Itoa(os.Getuid()), "sandboxd.sock")
}

func hostMemMB() int {
	if mb := memTotalMB(); mb > 0 {
		return mb
	}
	return 8192
}

// Default returns the built-in configuration for this host.
func Default() *Config {
	root := DefaultDataRoot()
	ncpu := runtime.NumCPU()
	return &Config{
		DataRoot: root,
		Socket:   DefaultSocket(root),
		LogLevel: "info",
		Budgets: Budgets{
			MaxVCPU: ncpu, VCPUOvercommit: 2.0, MaxMemMB: hostMemMB() / 2,
			MaxSandboxes: 8, MaxDiskGB: 50, MaxSnapshotGB: 30,
		},
		Defaults: Defaults{
			VCPU: 2, MemMB: 2048, DiskMB: 8192,
			IdleTimeoutS: 600, MaxLifetimeS: 8 * 3600, PausedTTLS: 24 * 3600,
			ExecTimeoutS: 600, MaxExecTimeoutS: 7200, StallWarnS: 300,
		},
		Queue:    Queue{MaxLen: 32, WaitForCapacityS: 30},
		WarmPool: WarmPool{Enabled: true, MinWarm: 1, MaxWarm: 3, IdleShrinkS: 1800},
		Network:  Network{DefaultMode: policy.ModeAllowlist, Allow: append([]string(nil), policy.DefaultAllow...)},
		Sync: Sync{
			Excludes: []string{"node_modules", ".venv", "venv", "target", "dist", "build", ".next", ".turbo",
				"__pycache__", ".pytest_cache", ".mypy_cache", ".gradle", ".DS_Store", ".cache"},
			SecretExcludes: []string{".env", ".env.*", "*.pem", "*.key", "id_rsa*", "id_ed25519*", "id_ecdsa*",
				".npmrc", ".pypirc", ".netrc", "*.p12", "*.pfx", ".aws", ".ssh"},
			MaxFileMB: 100,
		},
		Output: Output{HeadLines: 50, HeadBytes: 8 << 10, TailLines: 200, TailBytes: 24 << 10,
			MaxStoredBytes: 100 << 20, RingBytes: 64 << 10},
		Retention: Retention{EventsDays: 30, ExecLogsDays: 7, DestroyedRowsDays: 30, ArtifactsDays: 7,
			RecoveryHours: 24, SnapshotHours: 24},
		Health: Health{HeartbeatMS: 2000, UnhealthyAfterS: 6, LostAfterS: 20, DeadmanS: 600},
	}
}

// Path returns where config.yaml lives for a data root.
func Path(dataRoot string) string {
	if p := os.Getenv("SANDBOXD_CONFIG"); p != "" {
		return p
	}
	if runtime.GOOS == "linux" && os.Geteuid() == 0 {
		return "/etc/sandboxd/config.yaml"
	}
	return filepath.Join(dataRoot, "config.yaml")
}

// Load returns defaults overlaid with the YAML file at path (if present).
func Load(path string) (*Config, error) {
	c := Default()
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return c, c.Validate()
	}
	if err != nil {
		return nil, err
	}
	if err := yaml.Unmarshal(b, c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if c.Socket == "" {
		c.Socket = DefaultSocket(c.DataRoot)
	}
	return c, c.Validate()
}

// Validate checks invariants.
func (c *Config) Validate() error {
	if c.DataRoot == "" || !filepath.IsAbs(c.DataRoot) {
		return fmt.Errorf("data_root must be an absolute path, got %q", c.DataRoot)
	}
	if c.Budgets.MaxMemMB <= 0 || c.Budgets.MaxVCPU <= 0 {
		return errors.New("budgets must be positive")
	}
	if _, err := policy.Compile(policy.Spec{Mode: c.Network.DefaultMode, Allow: c.Network.Allow, Deny: c.Network.Deny}); err != nil {
		return fmt.Errorf("network: %w", err)
	}
	return nil
}

// Dirs under the data root.
func (c *Config) Dir(parts ...string) string {
	return filepath.Join(append([]string{c.DataRoot}, parts...)...)
}

// Marshal renders the config as YAML (for `sandboxd config`).
func (c *Config) Marshal() ([]byte, error) { return yaml.Marshal(c) }
