package service

import (
	"context"
	"encoding/json"
	"path"
	"strings"

	"github.com/karthikponna/tryboxi/pkg/client"
)

const autoPullMeta = "_autopull"

// DefaultAutoPull are files a dependency change rewrites. The user approves
// them once (per sandbox or in the MCP config); after that `npm install x`
// in the VM updates the host's manifest and lockfile automatically.
var DefaultAutoPull = []string{
	"package.json", "package-lock.json", "npm-shrinkwrap.json", "yarn.lock", "pnpm-lock.yaml", "bun.lockb", "bun.lock",
	"requirements*.txt", "pyproject.toml", "poetry.lock", "uv.lock", "Pipfile", "Pipfile.lock",
	"go.mod", "go.sum", "Cargo.toml", "Cargo.lock", "Gemfile", "Gemfile.lock", "composer.json", "composer.lock",
}

func expandAutoPull(pats []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, p := range pats {
		p = strings.TrimSpace(p)
		list := []string{p}
		if p == "default" || p == "true" {
			list = DefaultAutoPull
		}
		for _, x := range list {
			if x != "" && !seen[x] && !strings.Contains(x, "..") && !strings.HasPrefix(x, "/") && !strings.HasPrefix(x, ".git") {
				seen[x] = true
				out = append(out, x)
			}
		}
	}
	return out
}

func (b *Box) autoPullPatterns() []string {
	b.mu.Lock()
	raw := b.row.Metadata
	b.mu.Unlock()
	var meta map[string]string
	_ = json.Unmarshal([]byte(raw), &meta)
	if v := meta[autoPullMeta]; v != "" {
		return strings.Split(v, ",")
	}
	return nil
}

func matchAutoPull(rel string, pats []string) bool {
	for _, p := range pats {
		if ok, _ := path.Match(p, rel); ok {
			return true
		}
		if ok, _ := path.Match(p, path.Base(rel)); ok {
			return true
		}
	}
	return false
}

// autoPull copies changed files that match the sandbox's approved patterns
// back to the host. Host files edited since the last sync are never
// overwritten (they are reported as skipped).
func (m *Manager) autoPull(ctx context.Context, b *Box, changed []string) *client.ApplyResult {
	pats := b.autoPullPatterns()
	if len(pats) == 0 || b.row.HostRepoPath == "" {
		return nil
	}
	var paths []string
	for _, c := range changed {
		if matchAutoPull(c, pats) {
			paths = append(paths, c)
		}
	}
	if len(paths) == 0 {
		return nil
	}
	b.lc.RLock()
	defer b.lc.RUnlock()
	res, err := m.pullLocked(ctx, b, paths, false)
	if err != nil {
		m.log.Warn("auto-pull failed", "sandbox", b.id, "err", err)
		return &client.ApplyResult{Files: []client.FileResult{{Path: strings.Join(paths, ","), Action: "skipped", Reason: err.Error()}}, Skipped: len(paths)}
	}
	m.bus.Emit(b.id, "", "workspace.autopulled", map[string]any{"applied": res.Applied, "skipped": res.Skipped, "backup": res.BackupArtifactID})
	return &res
}
