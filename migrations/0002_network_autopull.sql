-- +goose Up
ALTER TABLE executions ADD COLUMN network_denied TEXT NOT NULL DEFAULT '[]';
ALTER TABLE executions ADD COLUMN auto_pulled TEXT NOT NULL DEFAULT '{}';

-- +goose Down
ALTER TABLE executions DROP COLUMN auto_pulled;
ALTER TABLE executions DROP COLUMN network_denied;
