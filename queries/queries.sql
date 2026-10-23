-- name: InsertClient :exec
INSERT INTO clients (id, name, uid, token_hash, priority, max_sandboxes, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?);

-- name: GetClientByName :one
SELECT * FROM clients WHERE name = ?;

-- name: GetClientByTokenHash :one
SELECT * FROM clients WHERE token_hash = ?;

-- name: ListClients :many
SELECT * FROM clients ORDER BY created_at;

-- name: InsertTemplate :exec
INSERT INTO templates (id, name, spec_hash, spec, base_image, arch, backend, kernel_version, agent_version,
    repo_path, repo_commit, detected_from, rootfs_path, rootfs_size_bytes, status, build_log_path, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetTemplate :one
SELECT * FROM templates WHERE id = ?;

-- name: GetTemplateBySpecHash :one
SELECT * FROM templates WHERE spec_hash = ?;

-- name: GetReadyTemplateByName :one
SELECT * FROM templates WHERE name = ? AND status = 'ready' ORDER BY built_at DESC LIMIT 1;

-- name: GetLatestTemplateByName :one
SELECT * FROM templates WHERE name = ? ORDER BY created_at DESC LIMIT 1;

-- name: ListTemplates :many
SELECT * FROM templates ORDER BY created_at DESC;

-- name: UpdateTemplateStatus :exec
UPDATE templates SET status = ?, status_reason = ? WHERE id = ?;

-- name: SetTemplateReady :exec
UPDATE templates SET status = 'ready', status_reason = '', golden_snapshot_id = ?, rootfs_path = ?,
    rootfs_size_bytes = ?, kernel_version = ?, agent_version = ?, built_at = ? WHERE id = ?;

-- name: MarkOtherTemplatesStale :exec
UPDATE templates SET status = 'stale' WHERE name = ? AND id != ? AND status = 'ready';

-- name: DeleteTemplate :exec
DELETE FROM templates WHERE id = ?;

-- name: InsertSnapshot :exec
INSERT INTO snapshots (id, kind, template_id, source_sandbox_id, parent_snapshot_id, vmstate_path, mem_path,
    disk_path, dir_path, mem_size_bytes, disk_size_bytes, backend, vmm_version, cpu_template, kernel_version,
    agent_version, vcpu, mem_mb, network_enabled, machine_id, workspace_commit, sync_manifest_path, label,
    refcount, created_at, last_used_at, expires_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetSnapshot :one
SELECT * FROM snapshots WHERE id = ?;

-- name: ListSnapshots :many
SELECT * FROM snapshots
WHERE (sqlc.narg('source_sandbox_id') IS NULL OR source_sandbox_id = sqlc.narg('source_sandbox_id'))
  AND (sqlc.narg('kind') IS NULL OR kind = sqlc.narg('kind'))
ORDER BY created_at DESC;

-- name: LatestSnapshotForSandbox :one
SELECT * FROM snapshots WHERE source_sandbox_id = ? AND kind IN ('checkpoint', 'pause', 'fork_base')
ORDER BY created_at DESC LIMIT 1;

-- name: AddSnapshotRef :exec
UPDATE snapshots SET refcount = refcount + ?, last_used_at = ? WHERE id = ?;

-- name: SetSnapshotExpiry :exec
UPDATE snapshots SET expires_at = ? WHERE id = ?;

-- name: DeleteSnapshot :exec
DELETE FROM snapshots WHERE id = ?;

-- name: ListSnapshotGCCandidates :many
SELECT * FROM snapshots
WHERE kind != 'golden' AND refcount <= 0 AND (expires_at IS NOT NULL AND expires_at < ?)
ORDER BY last_used_at;

-- name: ListSnapshotsLRU :many
SELECT * FROM snapshots WHERE kind != 'golden' AND refcount <= 0 ORDER BY last_used_at;

-- name: SnapshotBytes :one
SELECT CAST(COALESCE(SUM(mem_size_bytes + disk_size_bytes), 0) AS INTEGER) AS total FROM snapshots;

-- name: InsertSandbox :exec
INSERT INTO sandboxes (id, client_id, session_key, template_id, state, vcpu, mem_mb, disk_mb, network_mode,
    network_policy, workspace_mode, host_repo_path, dir_path, idle_timeout_s, max_lifetime_s, created_at,
    last_active_at, expires_at, metadata)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetSandbox :one
SELECT * FROM sandboxes WHERE id = ?;

-- name: ListSandboxes :many
SELECT * FROM sandboxes
WHERE (sqlc.narg('client_id') IS NULL OR client_id = sqlc.narg('client_id'))
  AND (sqlc.narg('session_key') IS NULL OR session_key = sqlc.narg('session_key'))
  AND (sqlc.narg('state') IS NULL OR state = sqlc.narg('state'))
  AND (sqlc.narg('include_destroyed') IS NOT NULL OR state != 'destroyed')
ORDER BY created_at DESC
LIMIT sqlc.arg('limit');

-- name: ListLiveSandboxes :many
SELECT * FROM sandboxes WHERE state NOT IN ('destroyed') ORDER BY created_at;

-- name: UpdateSandboxState :exec
UPDATE sandboxes SET state = ?, state_reason = ? WHERE id = ?;

-- name: SetSandboxReady :exec
UPDATE sandboxes SET state = 'ready', state_reason = '', booted_from_snapshot_id = ?, ready_at = ?,
    last_active_at = ?, vmm_pid = ?, sync_manifest_path = ?, paused_at = NULL WHERE id = ?;

-- name: SetSandboxPaused :exec
UPDATE sandboxes SET state = 'paused', state_reason = ?, paused_at = ? WHERE id = ?;

-- name: SetSandboxDestroyed :exec
UPDATE sandboxes SET state = 'destroyed', state_reason = ?, destroyed_at = ? WHERE id = ?;

-- name: SetSandboxHealth :exec
UPDATE sandboxes SET health = ? WHERE id = ?;

-- name: TouchSandbox :exec
UPDATE sandboxes SET last_active_at = ? WHERE id = ?;

-- name: SetSandboxHeartbeat :exec
UPDATE sandboxes SET last_heartbeat_at = ? WHERE id = ?;

-- name: NextExecSeq :one
UPDATE sandboxes SET exec_count = exec_count + 1 WHERE id = ? RETURNING exec_count;

-- name: InsertExecution :exec
INSERT INTO executions (id, sandbox_id, seq_in_sandbox, idempotency_key, command, cwd, env_keys, tty,
    background, timeout_s, state, started_at, stdout_path, stderr_path)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetExecution :one
SELECT * FROM executions WHERE id = ?;

-- name: GetExecutionByIdempotencyKey :one
SELECT * FROM executions WHERE sandbox_id = ? AND idempotency_key = ?;

-- name: ListExecutions :many
SELECT * FROM executions
WHERE sandbox_id = ? AND (sqlc.narg('state') IS NULL OR state = sqlc.narg('state'))
ORDER BY seq_in_sandbox DESC LIMIT sqlc.arg('limit');

-- name: UpdateExecutionProgress :exec
UPDATE executions SET stdout_bytes = ?, stderr_bytes = ?, last_seq_acked = ? WHERE id = ?;

-- name: FinishExecution :exec
UPDATE executions SET state = ?, reason = ?, exit_code = ?, signal = ?, oom_killed = ?, timed_out = ?,
    finished_at = ?, duration_ms = ?, stdout_bytes = ?, stderr_bytes = ?, last_seq_acked = ?, cpu_ms = ?,
    max_rss_kb = ? WHERE id = ?;

-- name: SetExecutionChangedFiles :exec
UPDATE executions SET changed_files = ? WHERE id = ?;

-- name: MarkRunningExecutionsLost :exec
UPDATE executions SET state = 'lost', reason = ?, finished_at = ? WHERE sandbox_id = ? AND state IN ('queued', 'running');

-- name: ListRunningExecutions :many
SELECT * FROM executions WHERE state IN ('queued', 'running');

-- name: InsertArtifact :exec
INSERT INTO artifacts (id, sandbox_id, execution_id, kind, path, sha256, size_bytes, meta, created_at, expires_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetArtifact :one
SELECT * FROM artifacts WHERE id = ?;

-- name: ListArtifacts :many
SELECT * FROM artifacts
WHERE (sqlc.narg('sandbox_id') IS NULL OR sandbox_id = sqlc.narg('sandbox_id'))
  AND (sqlc.narg('kind') IS NULL OR kind = sqlc.narg('kind'))
ORDER BY created_at DESC LIMIT sqlc.arg('limit');

-- name: ListExpiredArtifacts :many
SELECT * FROM artifacts WHERE expires_at IS NOT NULL AND expires_at < ?;

-- name: DeleteArtifact :exec
DELETE FROM artifacts WHERE id = ?;

-- name: InsertEvent :one
INSERT INTO events (ts, sandbox_id, execution_id, type, data) VALUES (?, ?, ?, ?, ?) RETURNING id;

-- name: ListEventsAfter :many
SELECT * FROM events
WHERE id > sqlc.arg('after')
  AND (sqlc.narg('sandbox_id') IS NULL OR sandbox_id = sqlc.narg('sandbox_id'))
ORDER BY id LIMIT sqlc.arg('limit');

-- name: DeleteEventsBefore :exec
DELETE FROM events WHERE ts < ?;

-- name: ListDestroyedSandboxesBefore :many
SELECT * FROM sandboxes WHERE state = 'destroyed' AND destroyed_at < ?;

-- name: DeleteSandboxRow :exec
DELETE FROM sandboxes WHERE id = ?;

-- name: ListFinishedExecutionsBefore :many
SELECT * FROM executions WHERE finished_at IS NOT NULL AND finished_at < ?;

-- name: DeleteExecution :exec
DELETE FROM executions WHERE id = ?;

-- name: InsertNetworkPolicy :exec
INSERT INTO network_policies (id, name, default_action, allow, deny, created_at) VALUES (?, ?, ?, ?, ?, ?);

-- name: GetNetworkPolicyByName :one
SELECT * FROM network_policies WHERE name = ?;

-- name: GetNetworkPolicy :one
SELECT * FROM network_policies WHERE id = ?;

-- name: SetExecutionExtras :exec
UPDATE executions SET network_denied = ?, auto_pulled = ? WHERE id = ?;

-- name: UpdateSandboxNetwork :exec
UPDATE sandboxes SET network_mode = ?, network_policy = ? WHERE id = ?;

-- name: UpdateSandboxMetadata :exec
UPDATE sandboxes SET metadata = ? WHERE id = ?;
