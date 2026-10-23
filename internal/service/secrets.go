package service

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	guestv1 "github.com/karthikponna/tryboxi/gen/guest/v1"
	"github.com/karthikponna/tryboxi/internal/backend"
	"github.com/karthikponna/tryboxi/internal/guestclient"
	"github.com/karthikponna/tryboxi/internal/network/egress"
	"github.com/karthikponna/tryboxi/internal/secrets"
	"github.com/karthikponna/tryboxi/pkg/client"
)

const (
	caGuestPath     = "/usr/local/share/ca-certificates/sandboxd-proxy.crt"
	caGuestBundle   = "/etc/ssl/certs/ca-certificates.crt"
	secretMetaPrefx = "secret:"
)

// PutSecret stores a host-side secret. Its value is never returned.
func (m *Manager) PutSecret(ctx context.Context, req client.SecretRequest) (client.SecretInfo, error) {
	s := secrets.Secret{Name: req.Name, Kind: req.Kind, Value: req.Value, Hosts: req.Hosts, Repos: req.Repos, PushBranches: req.PushBranches}
	if err := m.secrets.Put(s); err != nil {
		return client.SecretInfo{}, errInvalid("%v", err)
	}
	x, _ := m.secrets.Get(s.Name)
	m.bus.Emit("", "", "secret.stored", map[string]any{"name": x.Name, "kind": x.Kind, "hosts": x.Hosts})
	// Running sandboxes that use it pick up the new value immediately.
	for _, b := range m.liveBoxes() {
		b.refreshSecrets()
	}
	return secretView(x.Public()), nil
}

// DeleteSecret removes a secret; sandboxes holding its placeholder can no
// longer use it.
func (m *Manager) DeleteSecret(ctx context.Context, name string) error {
	if err := m.secrets.Delete(name); err != nil {
		return errNotFound("secret", name)
	}
	m.bus.Emit("", "", "secret.deleted", map[string]string{"name": name})
	for _, b := range m.liveBoxes() {
		b.refreshSecrets()
	}
	return nil
}

// ListSecrets lists secrets without values.
func (m *Manager) ListSecrets(ctx context.Context) []client.SecretInfo {
	var out []client.SecretInfo
	for _, p := range m.secrets.List() {
		out = append(out, secretView(p))
	}
	return out
}

func secretView(p secrets.Public) client.SecretInfo {
	return client.SecretInfo{Name: p.Name, Kind: p.Kind, Hosts: p.Hosts, Repos: p.Repos, PushBranches: p.PushBranches,
		Preview: p.Preview, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt}
}

// secretMeta assigns fresh placeholders for the requested secrets.
func (m *Manager) secretMeta(names []string, meta map[string]string) error {
	for _, n := range names {
		if _, ok := m.secrets.Get(n); !ok {
			return errInvalid("unknown secret %q (add it with `sandboxctl secret set %s ...`)", n, n)
		}
		meta[secretMetaPrefx+n] = secrets.Placeholder(n)
	}
	return nil
}

// bindings returns the secrets bound to this sandbox with current values.
func (b *Box) bindings() []egress.Binding {
	b.mu.Lock()
	metaJSON := b.row.Metadata
	b.mu.Unlock()
	var meta map[string]string
	_ = json.Unmarshal([]byte(metaJSON), &meta)
	var out []egress.Binding
	for k, ph := range meta {
		name, ok := cutPrefix(k, secretMetaPrefx)
		if !ok {
			continue
		}
		if s, ok := b.m.secrets.Get(name); ok {
			out = append(out, egress.Binding{Placeholder: ph, Secret: s})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Secret.Name < out[j].Secret.Name })
	return out
}

func (b *Box) refreshSecrets() {
	b.mu.Lock()
	nw := b.net
	b.mu.Unlock()
	if nw != nil {
		nw.SetSecrets(b.bindings())
	}
}

// newNet creates the VM network with the sandbox's policy and secrets.
func (m *Manager) newNet(b *Box, events bool) (backend.Net, error) {
	o := backend.NetOptions{
		Policy: b.policy(), Log: m.log, CA: m.ca, LogAllowed: m.cfg.Network.LogAllowed,
		Limits: egress.Limits{MaxConns: m.cfg.Network.MaxConns, ConnsPerMinute: m.cfg.Network.ConnsPerMinute,
			BytesPerSec: m.cfg.Network.EgressMbps * 1e6 / 8},
	}
	if events {
		o.OnEvent = m.netEventSink(b.id)
	}
	nw, err := m.be.NewNet(o)
	if err != nil {
		return nil, err
	}
	nw.SetSecrets(b.bindings())
	return nw, nil
}

// guestEnv is the default environment of every command in the sandbox:
// placeholders for bound secrets, the proxy CA, and git rewrites so SSH
// remotes go over HTTPS through the git proxy.
func (m *Manager) guestEnv(b *Box) map[string]string {
	env := map[string]string{"SANDBOX_ID": b.id}
	if m.ca != nil {
		env["NODE_EXTRA_CA_CERTS"] = caGuestPath
		env["SSL_CERT_FILE"] = caGuestBundle
		env["REQUESTS_CA_BUNDLE"] = caGuestBundle
	}
	var gitHosts []string
	for _, bd := range b.bindings() {
		switch bd.Secret.Kind {
		case secrets.KindEnv:
			env[bd.Secret.Name] = bd.Placeholder
		case secrets.KindGit:
			for _, h := range bd.Secret.Hosts {
				if !strings.HasPrefix(h, "*.") {
					gitHosts = append(gitHosts, h)
				}
			}
		}
	}
	if len(gitHosts) > 0 {
		i := 0
		for _, h := range gitHosts {
			for _, from := range []string{"git@" + h + ":", "ssh://git@" + h + "/"} {
				env["GIT_CONFIG_KEY_"+strconv.Itoa(i)] = "url.https://" + h + "/.insteadOf"
				env["GIT_CONFIG_VALUE_"+strconv.Itoa(i)] = from
				i++
			}
		}
		env["GIT_CONFIG_COUNT"] = strconv.Itoa(i)
		env["GIT_TERMINAL_PROMPT"] = "0"
	}
	return env
}

// ensureCA installs the proxy CA into the guest trust store if missing.
func (m *Manager) ensureCA(ctx context.Context, gc *guestclient.Client) error {
	if m.ca == nil {
		return nil
	}
	st, err := gc.Stat(ctx, &guestv1.StatRequest{Path: caGuestPath})
	if err == nil && st.Exists && st.Size == uint64(len(m.ca.PEM)) {
		return nil
	}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	_ = tw.WriteHeader(&tar.Header{Name: "sandboxd-proxy.crt", Mode: 0o644, Size: int64(len(m.ca.PEM)), ModTime: time.Now(), Typeflag: tar.TypeReg})
	_, _ = tw.Write(m.ca.PEM)
	_ = tw.Close()
	if _, err := putTarAs(ctx, gc, "/usr/local/share/ca-certificates", "root", nil, &buf); err != nil {
		return fmt.Errorf("install proxy CA: %w", err)
	}
	code, err := guestRunAs(ctx, gc, "root", "update-ca-certificates >/dev/null 2>&1", "/", 2*time.Minute, io.Discard)
	if err != nil || code != 0 {
		return fmt.Errorf("update-ca-certificates failed (exit %d): %v", code, err)
	}
	return nil
}

// checkLeak rejects input that carries a real secret value into the VM.
func (m *Manager) checkLeak(parts ...string) error {
	for _, v := range m.secrets.Values() {
		for _, p := range parts {
			if len(v) >= 8 && strings.Contains(p, string(v)) {
				return errInvalid("the request contains the value of a stored secret; secrets never enter sandboxes. " +
					"Bind the secret to the sandbox and use its environment variable (a placeholder the proxy swaps on the way out)")
			}
		}
	}
	return nil
}
