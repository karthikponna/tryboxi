package service

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/karthikponna/tryboxi/internal/network/policy"
	"github.com/karthikponna/tryboxi/internal/store/db"
	"github.com/karthikponna/tryboxi/pkg/client"
)

// denial is a blocked outbound attempt, kept per sandbox so results can
// tell the agent (and user) exactly what was blocked.
type denial struct {
	at     time.Time
	host   string
	reason string
}

const maxDenials = 100

func (b *Box) noteDenial(host, reason string) {
	if host == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.denials = append(b.denials, denial{at: time.Now(), host: host, reason: reason})
	if len(b.denials) > maxDenials {
		b.denials = b.denials[len(b.denials)-maxDenials:]
	}
}

// deniedSince returns unique blocked hosts since t.
func (b *Box) deniedSince(t time.Time) []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	seen := map[string]bool{}
	var out []string
	for _, d := range b.denials {
		if d.at.Before(t) || seen[d.host] {
			continue
		}
		seen[d.host] = true
		out = append(out, d.host)
	}
	sort.Strings(out)
	return out
}

// stackPresets adds presets a repo obviously needs beyond the defaults.
func stackPresets(repo string) []string {
	if repo == "" {
		return nil
	}
	var out []string
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join(repo, name))
		if err != nil || len(b) > 4<<20 {
			return ""
		}
		return string(b)
	}
	pkg := read("package.json")
	if strings.Contains(pkg, "playwright") || strings.Contains(pkg, "puppeteer") || strings.Contains(pkg, "cypress") {
		out = append(out, "@browsers")
	}
	py := read("requirements.txt") + read("pyproject.toml")
	if strings.Contains(py, "transformers") || strings.Contains(py, "huggingface") || strings.Contains(py, "diffusers") {
		out = append(out, "@huggingface")
	}
	return out
}

func (b *Box) policySpec() policy.Spec {
	b.mu.Lock()
	raw := b.row.NetworkPolicy
	mode := b.row.NetworkMode
	b.mu.Unlock()
	var spec policy.Spec
	_ = json.Unmarshal([]byte(raw), &spec)
	if spec.Mode == "" {
		spec.Mode = policy.Mode(mode)
	}
	return spec
}

// setPolicySpec persists spec and applies it to the live network.
func (m *Manager) setPolicySpec(ctx context.Context, b *Box, spec policy.Spec) error {
	p, err := policy.Compile(spec)
	if err != nil {
		return errInvalid("network: %v", err)
	}
	js, _ := json.Marshal(spec)
	if err := m.st.W.UpdateSandboxNetwork(ctx, db.UpdateSandboxNetworkParams{NetworkMode: string(spec.Mode), NetworkPolicy: string(js), ID: b.id}); err != nil {
		return err
	}
	b.mu.Lock()
	b.row.NetworkMode, b.row.NetworkPolicy = string(spec.Mode), string(js)
	nw := b.net
	b.mu.Unlock()
	if nw != nil {
		nw.SetPolicy(p)
	}
	return nil
}

// UpdateNetwork changes a sandbox's egress policy live (no restart). This
// is what an agent's "please allow host X" request lands on after the user
// approves it.
func (m *Manager) UpdateNetwork(ctx context.Context, id string, req client.NetworkUpdate) (client.NetworkInfo, error) {
	b, err := m.box(id)
	if err != nil {
		return client.NetworkInfo{}, err
	}
	spec := b.policySpec()
	if req.Mode != "" {
		spec.Mode = policy.Mode(req.Mode)
	}
	add := func(list []string, items []string) []string {
		for _, it := range items {
			it = strings.ToLower(strings.TrimSpace(it))
			if it == "" {
				continue
			}
			dup := false
			for _, x := range list {
				dup = dup || x == it
			}
			if !dup {
				list = append(list, it)
			}
		}
		return list
	}
	spec.Allow = add(spec.Allow, req.Allow)
	spec.Deny = add(spec.Deny, req.Deny)
	if len(req.Remove) > 0 {
		rm := map[string]bool{}
		for _, r := range req.Remove {
			rm[strings.ToLower(r)] = true
		}
		var keep []string
		for _, a := range spec.Allow {
			if !rm[a] {
				keep = append(keep, a)
			}
		}
		spec.Allow = keep
	}
	if err := m.setPolicySpec(ctx, b, spec); err != nil {
		return client.NetworkInfo{}, err
	}
	m.bus.Emit(id, "", "network.changed", map[string]any{"mode": spec.Mode, "allowed": req.Allow, "denied": req.Deny, "reason": req.Reason})
	return m.NetworkInfo(ctx, id)
}

// NetworkInfo returns the current policy and recent denials.
func (m *Manager) NetworkInfo(ctx context.Context, id string) (client.NetworkInfo, error) {
	b, err := m.box(id)
	if err != nil {
		return client.NetworkInfo{}, err
	}
	spec := b.policySpec()
	info := client.NetworkInfo{Mode: string(spec.Mode), Allow: spec.Allow, Deny: spec.Deny, Ports: spec.Ports}
	b.mu.Lock()
	for i := len(b.denials) - 1; i >= 0 && len(info.RecentDenied) < 30; i-- {
		d := b.denials[i]
		info.RecentDenied = append(info.RecentDenied, client.DeniedConn{Host: d.host, Reason: d.reason, At: d.at.UnixMilli()})
	}
	b.mu.Unlock()
	return info, nil
}

// runSetup runs setup commands with the setup network policy, then locks
// the sandbox down to its normal policy.
func (m *Manager) runSetup(ctx context.Context, b *Box, steps []string, setupMode string) ([]client.SetupStep, error) {
	agentSpec := b.policySpec()
	setupSpec := agentSpec
	if setupMode == "" {
		setupMode = string(policy.ModeFull)
	}
	setupSpec.Mode = policy.Mode(setupMode)
	if err := m.setPolicySpec(ctx, b, setupSpec); err != nil {
		return nil, err
	}
	m.bus.Emit(b.id, "", "network.setup_phase", map[string]string{"mode": setupMode})
	var out []client.SetupStep
	var runErr error
	for _, cmd := range steps {
		e, err := m.Exec(ctx, b.id, client.ExecRequest{Cmd: cmd, TimeoutS: 1800, Sync: client.Bool(false)}, "")
		step := client.SetupStep{Cmd: cmd, ExecID: e.ID, Status: e.Status, ExitCode: e.ExitCode}
		out = append(out, step)
		if err != nil {
			runErr = err
			break
		}
		if e.ExitCode == nil || *e.ExitCode != 0 {
			break
		}
	}
	if err := m.setPolicySpec(context.Background(), b, agentSpec); err != nil {
		return out, err
	}
	m.bus.Emit(b.id, "", "network.locked", map[string]string{"mode": string(agentSpec.Mode)})
	return out, runErr
}
