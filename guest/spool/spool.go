// Package spool keeps every output frame of an execution on the guest disk
// until the host acknowledges it, so a dropped host connection never loses
// output. Frames are length-prefixed records in one append-only file.
package spool

import (
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
)

// Spool is an append-only frame log for one execution.
type Spool struct {
	mu      sync.Mutex
	f       *os.File
	path    string
	offsets []int64 // offsets[i] is the file offset of frame seq i+1
	size    int64
	acked   uint64
	closed  bool
	cond    *sync.Cond
}

// Open creates (truncating) the spool file for execID under dir.
func Open(dir, execID string) (*Spool, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	p := filepath.Join(dir, filepath.Base(execID)+".spool")
	f, err := os.OpenFile(p, os.O_CREATE|os.O_TRUNC|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	s := &Spool{f: f, path: p}
	s.cond = sync.NewCond(&s.mu)
	return s, nil
}

// Append stores a frame and returns its sequence number (1-based).
func (s *Spool) Append(rec []byte) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0, errors.New("spool: closed")
	}
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(rec)))
	if _, err := s.f.WriteAt(hdr[:], s.size); err != nil {
		return 0, err
	}
	if _, err := s.f.WriteAt(rec, s.size+4); err != nil {
		return 0, err
	}
	s.offsets = append(s.offsets, s.size)
	s.size += 4 + int64(len(rec))
	s.cond.Broadcast()
	return uint64(len(s.offsets)), nil
}

// NextSeq returns the sequence number the next Append will get.
func (s *Spool) NextSeq() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return uint64(len(s.offsets)) + 1
}

// Read returns the frame with sequence seq. If it doesn't exist yet and the
// spool is open, Read blocks until it does or until done is closed.
func (s *Spool) Read(seq uint64, done <-chan struct{}) ([]byte, error) {
	s.mu.Lock()
	for uint64(len(s.offsets)) < seq {
		if s.closed {
			s.mu.Unlock()
			return nil, io.EOF
		}
		select {
		case <-done:
			s.mu.Unlock()
			return nil, io.EOF
		default:
		}
		s.cond.Wait()
	}
	off := s.offsets[seq-1]
	f := s.f
	s.mu.Unlock()
	var hdr [4]byte
	if _, err := f.ReadAt(hdr[:], off); err != nil {
		return nil, err
	}
	buf := make([]byte, binary.BigEndian.Uint32(hdr[:]))
	if _, err := f.ReadAt(buf, off+4); err != nil {
		return nil, err
	}
	return buf, nil
}

// Wake releases blocked readers so they can observe a closed done channel.
func (s *Spool) Wake() {
	s.mu.Lock()
	s.cond.Broadcast()
	s.mu.Unlock()
}

// Ack records that the host durably stored frames up to seq.
func (s *Spool) Ack(seq uint64) {
	s.mu.Lock()
	if seq > s.acked {
		s.acked = seq
	}
	s.mu.Unlock()
}

// Acked returns the highest acknowledged sequence number.
func (s *Spool) Acked() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.acked
}

// Seal marks the spool complete: no more frames will be appended.
func (s *Spool) Seal() {
	s.mu.Lock()
	s.closed = true
	s.cond.Broadcast()
	s.mu.Unlock()
}

// Remove deletes the spool file. Called once every frame is acked.
func (s *Spool) Remove() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	s.cond.Broadcast()
	_ = s.f.Close()
	return os.Remove(s.path)
}
