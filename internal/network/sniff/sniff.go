// Package sniff reads the first bytes of a client connection to find the
// intended hostname: TLS ClientHello SNI or the HTTP/1.x Host header.
package sniff

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// Kind of protocol detected.
type Kind string

const (
	KindTLS     Kind = "tls"
	KindHTTP    Kind = "http"
	KindUnknown Kind = ""
)

const maxPeek = 16 * 1024

// Result is what was learned from the peeked bytes.
type Result struct {
	Kind Kind
	Host string
	// Buffered holds every byte read from the client; it must be written to
	// the upstream connection before splicing.
	Buffered []byte
}

// Peek reads from c until it can identify the host or gives up.
func Peek(c net.Conn, timeout time.Duration) (Result, error) {
	_ = c.SetReadDeadline(time.Now().Add(timeout))
	defer c.SetReadDeadline(time.Time{})
	var buf []byte
	tmp := make([]byte, 4096)
	for len(buf) < maxPeek {
		n, err := c.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if len(buf) > 0 {
			if buf[0] == 0x16 {
				host, complete, perr := parseClientHello(buf)
				if perr != nil {
					return Result{Kind: KindTLS, Buffered: buf}, perr
				}
				if complete {
					return Result{Kind: KindTLS, Host: host, Buffered: buf}, nil
				}
			} else if looksHTTP(buf) {
				if i := bytes.Index(buf, []byte("\r\n\r\n")); i >= 0 {
					return Result{Kind: KindHTTP, Host: httpHost(buf[:i+4]), Buffered: buf}, nil
				}
			} else {
				return Result{Kind: KindUnknown, Buffered: buf}, nil
			}
		}
		if err != nil {
			return Result{Buffered: buf}, err
		}
	}
	return Result{Buffered: buf}, errors.New("sniff: no host found in first 16KiB")
}

var methods = []string{"GET ", "POST ", "PUT ", "HEAD ", "DELETE ", "OPTIONS ", "PATCH ", "CONNECT ", "TRACE "}

func looksHTTP(b []byte) bool {
	s := string(b[:min(len(b), 8)])
	for _, m := range methods {
		if strings.HasPrefix(m, s) || strings.HasPrefix(s, m) {
			return true
		}
	}
	return false
}

func httpHost(head []byte) string {
	req, err := http.ReadRequest(bufio.NewReader(bytes.NewReader(head)))
	if err != nil {
		return ""
	}
	h := req.Host
	if host, _, err := net.SplitHostPort(h); err == nil {
		h = host
	}
	return strings.ToLower(h)
}

// parseClientHello extracts SNI. complete is false if more bytes are needed.
func parseClientHello(b []byte) (host string, complete bool, err error) {
	if len(b) < 5 {
		return "", false, nil
	}
	if b[0] != 0x16 {
		return "", true, errors.New("not a TLS handshake")
	}
	recLen := int(binary.BigEndian.Uint16(b[3:5]))
	if len(b) < 5+recLen {
		return "", false, nil
	}
	hs := b[5 : 5+recLen]
	if len(hs) < 4 || hs[0] != 0x01 {
		return "", true, errors.New("not a ClientHello")
	}
	hsLen := int(hs[1])<<16 | int(hs[2])<<8 | int(hs[3])
	// A ClientHello spanning several records is parsed from the first only.
	body := hs[4:]
	if len(body) > hsLen {
		body = body[:hsLen]
	}
	r := reader{b: body}
	r.skip(2 + 32) // version + random
	r.skip(int(r.u8()))
	r.skip(int(r.u16()))
	r.skip(int(r.u8()))
	if r.err != nil {
		return "", true, r.err
	}
	if r.left() == 0 {
		return "", true, nil
	}
	ext := reader{b: r.bytes(int(r.u16()))}
	for ext.left() >= 4 && ext.err == nil {
		typ := ext.u16()
		data := reader{b: ext.bytes(int(ext.u16()))}
		if typ != 0 {
			continue
		}
		list := reader{b: data.bytes(int(data.u16()))}
		for list.left() >= 3 && list.err == nil {
			nameType := list.u8()
			name := list.bytes(int(list.u16()))
			if nameType == 0 {
				return strings.ToLower(string(name)), true, nil
			}
		}
	}
	if ext.err != nil {
		return "", true, ext.err
	}
	return "", true, nil
}

type reader struct {
	b   []byte
	err error
}

func (r *reader) left() int { return len(r.b) }
func (r *reader) fail()     { r.err = io.ErrUnexpectedEOF; r.b = nil }
func (r *reader) skip(n int) {
	if r.err != nil || n > len(r.b) {
		r.fail()
		return
	}
	r.b = r.b[n:]
}
func (r *reader) u8() uint8 {
	if r.err != nil || len(r.b) < 1 {
		r.fail()
		return 0
	}
	v := r.b[0]
	r.b = r.b[1:]
	return v
}
func (r *reader) u16() uint16 {
	if r.err != nil || len(r.b) < 2 {
		r.fail()
		return 0
	}
	v := binary.BigEndian.Uint16(r.b)
	r.b = r.b[2:]
	return v
}
func (r *reader) bytes(n int) []byte {
	if r.err != nil || n > len(r.b) {
		r.fail()
		return nil
	}
	v := r.b[:n]
	r.b = r.b[n:]
	return v
}
