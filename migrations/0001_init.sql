-- +goose Up
CREATE TABLE clients (
    id            TEXT PRIMARY KEY,
    name          TEXT NOT NULL,
    uid           INTEGER,
    token_hash    TEXT,
    priority      INTEGER NOT NULL DEFAULT 0,
    max_sandboxes INTEGER NOT NULL DEFAULT 0,
    created_at    INTEGER NOT NULL
);
CREATE UNIQUE INDEX clients_name ON clients(name);

CREATE TABLE network_policies (
    id             TEXT PRIMARY KEY,
    name           TEXT NOT NULL UNIQUE,
    default_action TEXT NOT NULL,
    allow          TEXT NOT NULL DEFAULT '[]',
    deny           TEXT NOT NULL DEFAULT '[]',
    created_at     INTEGER NOT NULL
);

CREATE TABLE templates (
    id                 TEXT PRIMARY KEY,
    name               TEXT NOT NULL,
    spec_hash          TEXT NOT NULL UNIQUE,
    spec               TEXT NOT NULL DEFAULT '{}',
    base_image         TEXT NOT NULL DEFAULT '',
    arch               TEXT NOT NULL,
    backend            TEXT NOT NULL,
    kernel_version     TEXT NOT NULL DEFAULT '',
    agent_version      TEXT NOT NULL DEFAULT '',
    repo_path          TEXT NOT NULL DEFAULT '',
    repo_commit        TEXT NOT NULL DEFAULT '',
    detected_from      TEXT NOT NULL DEFAULT '',
    rootfs_path        TEXT NOT NULL DEFAULT '',
    rootfs_size_bytes  INTEGER NOT NULL DEFAULT 0,
    golden_snapshot_id TEXT,
    status             TEXT NOT NULL,
    status_reason      TEXT NOT NULL DEFAULT '',
    build_log_path     TEXT NOT NULL DEFAULT '',
    created_at         INTEGER NOT NULL,
    built_at           INTEGER
);
CREATE INDEX templates_name ON templates(name, status);

CREATE TABLE snapshots (
    id                 TEXT PRIMARY KEY,
    kind               TEXT NOT NULL,
    template_id        TEXT,
    source_sandbox_id  TEXT,
    parent_snapshot_id TEXT,
    vmstate_path       TEXT NOT NULL,
    mem_path           TEXT NOT NULL DEFAULT '',
    disk_path          TEXT NOT NULL,
    dir_path           TEXT NOT NULL,
    mem_size_bytes     INTEGER NOT NULL DEFAULT 0,
    disk_size_bytes    INTEGER NOT NULL DEFAULT 0,
    backend            TEXT NOT NULL,
    vmm_version        TEXT NOT NULL,
    cpu_template       TEXT NOT NULL DEFAULT '',
    kernel_version     TEXT NOT NULL,
    agent_version      TEXT NOT NULL,
    vcpu               INTEGER NOT NULL,
    mem_mb             INTEGER NOT NULL,
    network_enabled    INTEGER NOT NULL DEFAULT 0,
    machine_id         BLOB,
    workspace_commit   TEXT NOT NULL DEFAULT '',
    sync_manifest_path TEXT NOT NULL DEFAULT '',
    label              TEXT NOT NULL DEFAULT '',
    refcount           INTEGER NOT NULL DEFAULT 0,
    created_at         INTEGER NOT NULL,
    last_used_at       INTEGER NOT NULL,
    expires_at         INTEGER
);
CREATE INDEX snapshots_template_kind ON snapshots(template_id, kind);
CREATE INDEX snapshots_refcount_expires ON snapshots(refcount, expires_at);
CREATE INDEX snapshots_source ON snapshots(source_sandbox_id, kind);

CREATE TABLE sandboxes (
    id                      TEXT PRIMARY KEY,
    client_id               TEXT NOT NULL,
    session_key             TEXT NOT NULL DEFAULT '',
    template_id             TEXT NOT NULL,
    booted_from_snapshot_id TEXT,
    state                   TEXT NOT NULL,
    health                  TEXT NOT NULL DEFAULT 'healthy',
    state_reason            TEXT NOT NULL DEFAULT '',
    vcpu                    INTEGER NOT NULL,
    mem_mb                  INTEGER NOT NULL,
    disk_mb                 INTEGER NOT NULL,
    network_mode            TEXT NOT NULL,
    network_policy_id       TEXT,
    network_policy          TEXT NOT NULL DEFAULT '{}',
    workspace_mode          TEXT NOT NULL,
    host_repo_path          TEXT NOT NULL DEFAULT '',
    sync_manifest_path      TEXT NOT NULL DEFAULT '',
    vmm_pid                 INTEGER NOT NULL DEFAULT 0,
    vmm_api_socket          TEXT NOT NULL DEFAULT '',
    vsock_path              TEXT NOT NULL DEFAULT '',
    netns                   TEXT NOT NULL DEFAULT '',
    tap_name                TEXT NOT NULL DEFAULT '',
    dir_path                TEXT NOT NULL,
    idle_timeout_s          INTEGER NOT NULL,
    max_lifetime_s          INTEGER NOT NULL,
    exec_count              INTEGER NOT NULL DEFAULT 0,
    created_at              INTEGER NOT NULL,
    ready_at                INTEGER,
    last_active_at          INTEGER NOT NULL,
    last_heartbeat_at       INTEGER,
    paused_at               INTEGER,
    expires_at              INTEGER NOT NULL,
    destroyed_at            INTEGER,
    metadata                TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX sandboxes_state ON sandboxes(state);
CREATE INDEX sandboxes_session ON sandboxes(client_id, session_key) WHERE state != 'destroyed';
CREATE INDEX sandboxes_expires ON sandboxes(expires_at);
CREATE INDEX sandboxes_last_active ON sandboxes(last_active_at);

CREATE TABLE executions (
    id              TEXT PRIMARY KEY,
    sandbox_id      TEXT NOT NULL,
    seq_in_sandbox  INTEGER NOT NULL,
    idempotency_key TEXT,
    command         TEXT NOT NULL,
    cwd             TEXT NOT NULL DEFAULT '',
    env_keys        TEXT NOT NULL DEFAULT '[]',
    tty             INTEGER NOT NULL DEFAULT 0,
    background      INTEGER NOT NULL DEFAULT 0,
    timeout_s       INTEGER NOT NULL,
    state           TEXT NOT NULL,
    reason          TEXT NOT NULL DEFAULT '',
    exit_code       INTEGER,
    signal          INTEGER,
    oom_killed      INTEGER NOT NULL DEFAULT 0,
    timed_out       INTEGER NOT NULL DEFAULT 0,
    started_at      INTEGER NOT NULL,
    finished_at     INTEGER,
    duration_ms     INTEGER,
    stdout_path     TEXT NOT NULL,
    stderr_path     TEXT NOT NULL,
    stdout_bytes    INTEGER NOT NULL DEFAULT 0,
    stderr_bytes    INTEGER NOT NULL DEFAULT 0,
    last_seq_acked  INTEGER NOT NULL DEFAULT 0,
    changed_files   TEXT NOT NULL DEFAULT '[]',
    cpu_ms          INTEGER,
    max_rss_kb      INTEGER
);
CREATE INDEX executions_sandbox_seq ON executions(sandbox_id, seq_in_sandbox);
CREATE UNIQUE INDEX executions_idem ON executions(sandbox_id, idempotency_key) WHERE idempotency_key IS NOT NULL;
CREATE INDEX executions_state ON executions(state);

CREATE TABLE artifacts (
    id           TEXT PRIMARY KEY,
    sandbox_id   TEXT,
    execution_id TEXT,
    kind         TEXT NOT NULL,
    path         TEXT NOT NULL,
    sha256       TEXT NOT NULL DEFAULT '',
    size_bytes   INTEGER NOT NULL DEFAULT 0,
    meta         TEXT NOT NULL DEFAULT '{}',
    created_at   INTEGER NOT NULL,
    expires_at   INTEGER
);
CREATE INDEX artifacts_sandbox ON artifacts(sandbox_id, kind);

CREATE TABLE events (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    ts           INTEGER NOT NULL,
    sandbox_id   TEXT,
    execution_id TEXT,
    type         TEXT NOT NULL,
    data         TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX events_sandbox_ts ON events(sandbox_id, ts);
CREATE INDEX events_ts ON events(ts);

-- +goose Down
DROP TABLE events;
DROP TABLE artifacts;
DROP TABLE executions;
DROP TABLE sandboxes;
DROP TABLE snapshots;
DROP TABLE templates;
DROP TABLE network_policies;
DROP TABLE clients;
