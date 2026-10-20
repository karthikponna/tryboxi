// Package ids generates prefixed, time-ordered ULID identifiers.
package ids

import (
	"crypto/rand"
	"strings"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"
)

const (
	Client    = "cli_"
	Template  = "tpl_"
	Snapshot  = "snp_"
	Sandbox   = "sbx_"
	Execution = "exe_"
	Artifact  = "art_"
	Network   = "net_"
)

var (
	mu      sync.Mutex
	entropy = ulid.Monotonic(rand.Reader, 0)
)

// New returns prefix + a lowercase ULID.
func New(prefix string) string {
	mu.Lock()
	defer mu.Unlock()
	return prefix + strings.ToLower(ulid.MustNew(ulid.Timestamp(time.Now()), entropy).String())
}

// Has reports whether id carries the given prefix.
func Has(id, prefix string) bool { return strings.HasPrefix(id, prefix) && len(id) > len(prefix) }
