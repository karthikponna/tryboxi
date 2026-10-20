// Package output stores execution output durably on the host and prepares
// it for models: ANSI stripped, progress bars collapsed, head+tail truncated.
package output

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

var ansiRE = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(\x07|\x1b\\)|\x1b[@-Z\\-_]`)

// StripANSI removes CSI/OSC escape sequences.
func StripANSI(b []byte) []byte { return ansiRE.ReplaceAll(b, nil) }

// CollapseCR keeps only the final state of carriage-return-redrawn lines
// (progress bars): for each line, the text after the last '\r' wins.
func CollapseCR(b []byte) []byte {
	if !bytes.ContainsRune(b, '\r') {
		return b
	}
	lines := bytes.Split(b, []byte("\n"))
	for i, l := range lines {
		l = bytes.TrimSuffix(l, []byte("\r"))
		if j := bytes.LastIndexByte(l, '\r'); j >= 0 {
			l = l[j+1:]
		}
		lines[i] = l
	}
	return bytes.Join(lines, []byte("\n"))
}

// IsBinary reports whether b looks like binary data.
func IsBinary(b []byte) bool {
	sample := b[:min(len(b), 8192)]
	if bytes.IndexByte(sample, 0) >= 0 {
		return true
	}
	bad := 0
	for len(sample) > 0 {
		r, size := utf8.DecodeRune(sample)
		if r == utf8.RuneError && size == 1 {
			bad++
		}
		sample = sample[size:]
	}
	return bad > 32
}

// Clean prepares raw output for a model.
func Clean(b []byte, stripANSI bool) []byte {
	if stripANSI {
		b = StripANSI(b)
	}
	b = CollapseCR(b)
	return bytes.ToValidUTF8(b, []byte("\uFFFD"))
}

// Limits for Truncate.
type Limits struct {
	HeadLines, HeadBytes, TailLines, TailBytes int
}

// Truncate keeps the first HeadLines (≤HeadBytes) and last TailLines
// (≤TailBytes) of text with a marker in between. totalBytes is the full size
// of the stream on disk, used in the marker when text is itself a window.
func Truncate(text []byte, lim Limits, hint string) (out string, truncated bool) {
	if len(text) <= lim.HeadBytes+lim.TailBytes {
		lines := bytes.Count(text, []byte("\n"))
		if lines <= lim.HeadLines+lim.TailLines {
			return string(text), false
		}
	}
	lines := strings.SplitAfter(string(text), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	var head []string
	hb := 0
	for _, l := range lines {
		if len(head) >= lim.HeadLines || hb+len(l) > lim.HeadBytes {
			break
		}
		head = append(head, l)
		hb += len(l)
	}
	var tail []string
	tb := 0
	for i := len(lines) - 1; i >= len(head); i-- {
		l := lines[i]
		if len(tail) >= lim.TailLines || tb+len(l) > lim.TailBytes {
			break
		}
		tail = append([]string{l}, tail...)
		tb += len(l)
	}
	omitted := len(lines) - len(head) - len(tail)
	if omitted <= 0 {
		return string(text), false
	}
	// A single giant line: keep byte windows instead.
	if len(head) == 0 && len(tail) == 0 {
		h := string(text[:min(len(text), lim.HeadBytes)])
		t := string(text[max(0, len(text)-lim.TailBytes):])
		return fmt.Sprintf("%s\n... %d bytes omitted (%s) ...\n%s", h, len(text)-len(h)-len(t), hint, t), true
	}
	var b strings.Builder
	for _, l := range head {
		b.WriteString(l)
	}
	if len(head) > 0 && !strings.HasSuffix(head[len(head)-1], "\n") {
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "... %d lines omitted (%s) ...\n", omitted, hint)
	for _, l := range tail {
		b.WriteString(l)
	}
	return b.String(), true
}
