// Package image builds boot artifacts: the agent initramfs and disk images.
package image

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// cpio "newc" format writer, enough for a tiny initramfs.
type cpioWriter struct {
	w   io.Writer
	ino uint32
}

func (c *cpioWriter) pad(n int) error {
	if r := n % 4; r != 0 {
		_, err := c.w.Write(make([]byte, 4-r))
		return err
	}
	return nil
}

func (c *cpioWriter) entry(name string, mode uint32, data []byte, rmajor, rminor uint32) error {
	c.ino++
	nlink := uint32(1)
	if mode&0o170000 == 0o040000 {
		nlink = 2
	}
	hdr := fmt.Sprintf("070701%08X%08X%08X%08X%08X%08X%08X%08X%08X%08X%08X%08X%08X",
		c.ino, mode, 0, 0, nlink, 0, len(data), 0, 0, rmajor, rminor, len(name)+1, 0)
	if _, err := io.WriteString(c.w, hdr); err != nil {
		return err
	}
	if _, err := io.WriteString(c.w, name+"\x00"); err != nil {
		return err
	}
	if err := c.pad(len(hdr) + len(name) + 1); err != nil {
		return err
	}
	if _, err := c.w.Write(data); err != nil {
		return err
	}
	return c.pad(len(data))
}

// BuildInitrd writes an uncompressed cpio initramfs that contains only the
// agent (as /init), a marker file, and the directories it needs.
func BuildInitrd(agentPath, outPath string) error {
	agent, err := os.ReadFile(agentPath)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	c := &cpioWriter{w: &buf}
	dirs := []string{"dev", "proc", "sys", "newroot"}
	for _, d := range dirs {
		if err := c.entry(d, 0o040755, nil, 0, 0); err != nil {
			return err
		}
	}
	if err := c.entry("dev/console", 0o020600, nil, 5, 1); err != nil {
		return err
	}
	if err := c.entry(".sandboxd-initramfs", 0o100644, nil, 0, 0); err != nil {
		return err
	}
	if err := c.entry("init", 0o100755, agent, 0, 0); err != nil {
		return err
	}
	if err := c.entry("TRAILER!!!", 0, nil, 0, 0); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return err
	}
	tmp := outPath + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, outPath)
}
