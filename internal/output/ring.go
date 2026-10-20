package output

// Ring keeps the last N bytes written to it.
type Ring struct {
	buf  []byte
	size int
	full bool
	pos  int
}

func NewRing(size int) *Ring {
	if size <= 0 {
		size = 64 << 10
	}
	return &Ring{buf: make([]byte, size), size: size}
}

func (r *Ring) Write(p []byte) {
	if len(p) >= r.size {
		copy(r.buf, p[len(p)-r.size:])
		r.pos = 0
		r.full = true
		return
	}
	n := copy(r.buf[r.pos:], p)
	if n < len(p) {
		copy(r.buf, p[n:])
		r.full = true
	}
	r.pos = (r.pos + len(p)) % r.size
	if r.pos == 0 && len(p) > 0 {
		r.full = true
	}
}

// Bytes returns the buffered tail in order.
func (r *Ring) Bytes() []byte {
	if !r.full {
		return append([]byte(nil), r.buf[:r.pos]...)
	}
	out := make([]byte, 0, r.size)
	out = append(out, r.buf[r.pos:]...)
	return append(out, r.buf[:r.pos]...)
}
