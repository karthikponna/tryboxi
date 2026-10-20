package output

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"
)

// Stream names.
const (
	Stdout = "stdout"
	Stderr = "stderr"
)

// Frame is one chunk of output with its sequence number.
type Frame struct {
	Seq    uint64 `json:"seq"`
	Stream string `json:"stream"`
	Data   []byte `json:"-"`
	TsMs   int64  `json:"ts_ms"`
}

type indexEntry struct {
	seq    uint64
	stream uint8
	off    int64
	n      uint32
}

// Log is the durable, fan-out output store for one execution.
type Log struct {
	dir      string
	maxBytes int64

	mu       sync.Mutex
	files    [2]*os.File
	sizes    [2]int64
	stored   [2]int64 // bytes actually written (capped by maxBytes)
	rings    [2]*Ring
	index    []indexEntry
	subs     map[chan Frame]struct{}
	closed   bool
	lastSeq  uint64
	dirty    bool
	stopSync chan struct{}
}

func streamIdx(s string) int {
	if s == Stderr {
		return 1
	}
	return 0
}

// Paths returns the stdout and stderr log paths for an execution dir.
func Paths(dir string) (string, string) {
	return filepath.Join(dir, "stdout.log"), filepath.Join(dir, "stderr.log")
}

// Create opens a new log in dir.
func Create(dir string, maxBytes int64, ringBytes int) (*Log, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	l := &Log{dir: dir, maxBytes: maxBytes, subs: map[chan Frame]struct{}{}, stopSync: make(chan struct{})}
	so, se := Paths(dir)
	for i, p := range []string{so, se} {
		f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			return nil, err
		}
		l.files[i] = f
		l.rings[i] = NewRing(ringBytes)
	}
	go l.syncLoop()
	return l, nil
}

func (l *Log) syncLoop() {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-l.stopSync:
			return
		case <-t.C:
			l.mu.Lock()
			if l.dirty && !l.closed {
				for _, f := range l.files {
					_ = f.Sync()
				}
				l.dirty = false
			}
			l.mu.Unlock()
		}
	}
}

// Append stores a frame. Frames must arrive in seq order; duplicates
// (seq <= last) are ignored so guest replays are idempotent.
func (l *Log) Append(seq uint64, stream string, data []byte) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return errors.New("output: log closed")
	}
	if seq <= l.lastSeq {
		return nil
	}
	l.lastSeq = seq
	i := streamIdx(stream)
	off := l.stored[i]
	l.sizes[i] += int64(len(data))
	l.rings[i].Write(data)
	keep := data
	if l.stored[0]+l.stored[1]+int64(len(data)) > l.maxBytes {
		room := l.maxBytes - l.stored[0] - l.stored[1]
		if room < 0 {
			room = 0
		}
		keep = data[:room]
	}
	if len(keep) > 0 {
		if _, err := l.files[i].Write(keep); err != nil {
			return err
		}
		l.stored[i] += int64(len(keep))
		l.index = append(l.index, indexEntry{seq: seq, stream: uint8(i), off: off, n: uint32(len(keep))})
		l.dirty = true
	}
	f := Frame{Seq: seq, Stream: stream, Data: append([]byte(nil), data...), TsMs: time.Now().UnixMilli()}
	for ch := range l.subs {
		select {
		case ch <- f:
		default:
			// Slow subscriber: drop it; it can resume from the files.
			close(ch)
			delete(l.subs, ch)
		}
	}
	return nil
}

// Sizes returns total bytes seen per stream (including any not stored).
func (l *Log) Sizes() (int64, int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.sizes[0], l.sizes[1]
}

// LastSeq returns the highest stored sequence number.
func (l *Log) LastSeq() uint64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.lastSeq
}

// Ring returns a copy of the in-memory tail for a stream.
func (l *Log) Ring(stream string) []byte {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.rings[streamIdx(stream)].Bytes()
}

// Subscribe returns frames with seq > after (replayed from disk first),
// then live frames until the log closes or cancel is called.
func (l *Log) Subscribe(after uint64) (replay []Frame, live chan Frame, cancel func()) {
	l.mu.Lock()
	defer l.mu.Unlock()
	replay = l.replayLocked(after)
	live = make(chan Frame, 256)
	if l.closed {
		close(live)
		return replay, live, func() {}
	}
	l.subs[live] = struct{}{}
	return replay, live, func() {
		l.mu.Lock()
		if _, ok := l.subs[live]; ok {
			delete(l.subs, live)
			close(live)
		}
		l.mu.Unlock()
	}
}

func (l *Log) replayLocked(after uint64) []Frame {
	var out []Frame
	so, se := Paths(l.dir)
	readers := [2]*os.File{}
	for i, p := range []string{so, se} {
		f, err := os.Open(p)
		if err != nil {
			return nil
		}
		defer f.Close()
		readers[i] = f
	}
	for _, e := range l.index {
		if e.seq <= after {
			continue
		}
		buf := make([]byte, e.n)
		if _, err := readers[e.stream].ReadAt(buf, e.off); err != nil {
			continue
		}
		name := Stdout
		if e.stream == 1 {
			name = Stderr
		}
		out = append(out, Frame{Seq: e.seq, Stream: name, Data: buf})
	}
	return out
}

// Close flushes, fsyncs, writes the frame index, and ends subscriptions.
func (l *Log) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	l.closed = true
	close(l.stopSync)
	for ch := range l.subs {
		close(ch)
		delete(l.subs, ch)
	}
	var firstErr error
	for _, f := range l.files {
		if err := f.Sync(); err != nil && firstErr == nil {
			firstErr = err
		}
		f.Close()
	}
	if err := writeIndex(filepath.Join(l.dir, "frames.idx"), l.index); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

func writeIndex(path string, idx []indexEntry) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	var rec [21]byte
	for _, e := range idx {
		binary.BigEndian.PutUint64(rec[0:], e.seq)
		rec[8] = e.stream
		binary.BigEndian.PutUint64(rec[9:], uint64(e.off))
		binary.BigEndian.PutUint32(rec[17:], e.n)
		if _, err := w.Write(rec[:]); err != nil {
			f.Close()
			return err
		}
	}
	if err := w.Flush(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// ReplayFinished reads frames with seq > after from a closed log directory.
func ReplayFinished(dir string, after uint64) ([]Frame, error) {
	b, err := os.ReadFile(filepath.Join(dir, "frames.idx"))
	if err != nil {
		return nil, err
	}
	l := &Log{dir: dir}
	for i := 0; i+21 <= len(b); i += 21 {
		l.index = append(l.index, indexEntry{
			seq:    binary.BigEndian.Uint64(b[i:]),
			stream: b[i+8],
			off:    int64(binary.BigEndian.Uint64(b[i+9:])),
			n:      binary.BigEndian.Uint32(b[i+17:]),
		})
	}
	return l.replayLocked(after), nil
}

// ReadRange returns up to limit bytes of a stream log starting at offset.
func ReadRange(path string, offset, limit int64) ([]byte, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, 0, err
	}
	if offset < 0 {
		offset = max(0, fi.Size()+offset)
	}
	if limit <= 0 || limit > 1<<20 {
		limit = 1 << 20
	}
	buf := make([]byte, limit)
	n, err := f.ReadAt(buf, offset)
	if err != nil && err != io.EOF {
		return nil, fi.Size(), err
	}
	return buf[:n], fi.Size(), nil
}

// ReadTail returns at most n bytes from the end of the file.
func ReadTail(path string, n int64) ([]byte, int64, error) {
	return ReadRange(path, -n, n)
}

// Match is a grep hit.
type Match struct {
	Line int    `json:"line"`
	Text string `json:"text"`
}

// Grep searches a log file line by line (ANSI stripped) for pattern.
func Grep(path, pattern string, maxMatches int) ([]Match, bool, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, false, fmt.Errorf("bad pattern: %w", err)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	if maxMatches <= 0 {
		maxMatches = 200
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	var out []Match
	ln := 0
	for sc.Scan() {
		ln++
		line := CollapseCR(StripANSI(sc.Bytes()))
		if re.Match(line) {
			if len(out) >= maxMatches {
				return out, true, nil
			}
			txt := string(line)
			if len(txt) > 2000 {
				txt = txt[:2000] + "…"
			}
			out = append(out, Match{Line: ln, Text: txt})
		}
	}
	return out, false, sc.Err()
}
