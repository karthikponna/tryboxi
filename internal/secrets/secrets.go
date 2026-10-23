// Package secrets keeps credentials on the host. Sandboxes only ever see a
// random placeholder; the egress proxy swaps the placeholder for the real
// value on requests to the hosts the secret is bound to.
package secrets

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Kinds of secrets.
const (
	// KindEnv: exposed to the VM as an env var holding a placeholder; the
	// proxy replaces the placeholder in request headers to allowed hosts.
	KindEnv = "env"
	// KindGit: a token for git over HTTPS. The proxy adds it to git
	// requests for allowed repos and enforces push branch rules.
	KindGit = "git"
)

// Secret is one stored credential.
type Secret struct {
	Name         string   `json:"name"`
	Kind         string   `json:"kind"`
	Value        string   `json:"value"`
	Hosts        []string `json:"hosts"`
	Repos        []string `json:"repos,omitempty"`         // git: "owner/repo" globs allowed
	PushBranches []string `json:"push_branches,omitempty"` // git: branch globs a push may update; empty = read-only
	CreatedAt    int64    `json:"created_at"`
	UpdatedAt    int64    `json:"updated_at"`
}

// Public is a Secret without its value.
type Public struct {
	Name         string   `json:"name"`
	Kind         string   `json:"kind"`
	Hosts        []string `json:"hosts"`
	Repos        []string `json:"repos,omitempty"`
	PushBranches []string `json:"push_branches,omitempty"`
	Preview      string   `json:"preview"`
	CreatedAt    int64    `json:"created_at"`
	UpdatedAt    int64    `json:"updated_at"`
}

var nameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)

// Validate checks a secret before storing it.
func (s *Secret) Validate() error {
	if !nameRE.MatchString(s.Name) {
		return fmt.Errorf("secret name %q must look like an env var (letters, digits, underscore)", s.Name)
	}
	if s.Kind == "" {
		s.Kind = KindEnv
	}
	if s.Kind != KindEnv && s.Kind != KindGit {
		return fmt.Errorf("kind must be %q or %q", KindEnv, KindGit)
	}
	if len(s.Value) < 8 {
		return errors.New("secret value is too short (min 8 characters)")
	}
	if strings.ContainsAny(s.Value, "\r\n") {
		return errors.New("secret value must be a single line")
	}
	if s.Kind == KindGit && len(s.Hosts) == 0 {
		s.Hosts = []string{"github.com"}
	}
	if len(s.Hosts) == 0 {
		return errors.New("at least one host is required (where the secret may be sent)")
	}
	for i, h := range s.Hosts {
		h = strings.ToLower(strings.TrimSpace(h))
		if h == "" || strings.ContainsAny(h, "/: ") || (strings.Contains(h, "*") && !strings.HasPrefix(h, "*.")) {
			return fmt.Errorf("invalid host %q (use example.com or *.example.com)", h)
		}
		s.Hosts[i] = h
	}
	if s.Kind == KindGit && len(s.Repos) == 0 {
		return errors.New("git secrets need at least one --repo owner/name (or owner/*)")
	}
	return nil
}

// MatchHost reports whether host may receive the secret.
func (s *Secret) MatchHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, h := range s.Hosts {
		if h == host || (strings.HasPrefix(h, "*.") && strings.HasSuffix(host, h[1:])) {
			return true
		}
	}
	return false
}

// MatchRepo reports whether a git secret covers owner/repo.
func (s *Secret) MatchRepo(repo string) bool {
	repo = strings.ToLower(strings.TrimSuffix(repo, ".git"))
	for _, r := range s.Repos {
		if ok, _ := path.Match(strings.ToLower(r), repo); ok {
			return true
		}
	}
	return false
}

// MatchPushBranch reports whether a push may update refs/heads/<branch>.
func (s *Secret) MatchPushBranch(branch string) bool {
	for _, b := range s.PushBranches {
		if ok, _ := path.Match(b, branch); ok {
			return true
		}
	}
	return false
}

// Public strips the value.
func (s Secret) Public() Public {
	prev := "****"
	if len(s.Value) >= 12 {
		prev = s.Value[:4] + "…" + s.Value[len(s.Value)-2:]
	}
	return Public{Name: s.Name, Kind: s.Kind, Hosts: s.Hosts, Repos: s.Repos, PushBranches: s.PushBranches,
		Preview: prev, CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt}
}

// Store persists secrets in a 0600 JSON file inside the data root.
type Store struct {
	path string
	mu   sync.RWMutex
	m    map[string]Secret
}

// Open loads (or creates) the store.
func Open(p string) (*Store, error) {
	s := &Store{path: p, m: map[string]Secret{}}
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	var list []Secret
	if err := json.Unmarshal(b, &list); err != nil {
		return nil, fmt.Errorf("%s: %w", p, err)
	}
	for _, x := range list {
		s.m[x.Name] = x
	}
	return s, nil
}

func (s *Store) saveLocked() error {
	list := make([]Secret, 0, len(s.m))
	for _, x := range s.m {
		list = append(list, x)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	b, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// Put stores or replaces a secret.
func (s *Store) Put(x Secret) error {
	if err := x.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UnixMilli()
	if old, ok := s.m[x.Name]; ok {
		x.CreatedAt = old.CreatedAt
	} else {
		x.CreatedAt = now
	}
	x.UpdatedAt = now
	s.m[x.Name] = x
	return s.saveLocked()
}

// Delete removes a secret.
func (s *Store) Delete(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.m[name]; !ok {
		return fmt.Errorf("secret %s not found", name)
	}
	delete(s.m, name)
	return s.saveLocked()
}

// Get returns a secret by name.
func (s *Store) Get(name string) (Secret, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	x, ok := s.m[name]
	return x, ok
}

// List returns all secrets without values.
func (s *Store) List() []Public {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Public, 0, len(s.m))
	for _, x := range s.m {
		out = append(out, x.Public())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Values returns every secret value (for leak scanning).
func (s *Store) Values() [][]byte {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([][]byte, 0, len(s.m))
	for _, x := range s.m {
		out = append(out, []byte(x.Value))
	}
	return out
}

// Placeholder makes the stand-in value a sandbox sees for a secret.
func Placeholder(name string) string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return "sbxph_" + strings.ToLower(name) + "_" + hex.EncodeToString(b)
}

// IsPlaceholder reports whether s looks like a placeholder.
func IsPlaceholder(s string) bool { return strings.HasPrefix(s, "sbxph_") }
