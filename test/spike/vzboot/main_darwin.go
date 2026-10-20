//go:build darwin

// Command vzboot is a manual spike: boot a VM with Virtualization.framework,
// talk to the guest agent over vsock, snapshot it, and restore two clones.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"

	guestv1 "github.com/karthikponna/tryboxi/gen/guest/v1"
	"github.com/karthikponna/tryboxi/internal/guestclient"
	"github.com/karthikponna/tryboxi/internal/image"
	"github.com/karthikponna/tryboxi/internal/vmm"
	"github.com/karthikponna/tryboxi/internal/vmm/vz"
)

func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context, c *guestclient.Client, id, cmd string) {
	st, err := c.Exec(ctx, &guestv1.ExecRequest{ExecId: id, Command: cmd, TimeoutS: 60})
	must(err)
	for {
		f, err := st.Recv()
		if err == io.EOF {
			return
		}
		must(err)
		switch k := f.Kind.(type) {
		case *guestv1.ExecFrame_Output:
			os.Stdout.Write(k.Output.Data)
		case *guestv1.ExecFrame_Exit:
			fmt.Printf("[%s exit=%d dur=%dms]\n", id, k.Exit.ExitCode, k.Exit.DurationMs)
			_, _ = c.Ack(ctx, &guestv1.AckRequest{ExecId: id, Seq: f.Seq})
			return
		}
	}
}

func main() {
	root := flag.String("root", "_out", "artifact root")
	work := flag.String("work", "", "work dir (default: temp)")
	flag.Parse()
	ctx := context.Background()

	w := *work
	if w == "" {
		var err error
		w, err = os.MkdirTemp("", "vzspike-")
		must(err)
	}
	log.Printf("work dir %s", w)
	kernel := filepath.Join(*root, "dl/opt/kata/share/kata-containers/vmlinux-6.12.28-153")
	initrd := filepath.Join(w, "initrd.cpio")
	must(image.BuildInitrd(filepath.Join(*root, "bin/sandbox-agent-linux-arm64"), initrd))

	d := vz.New()
	disk := filepath.Join(w, "root.img")
	must(d.CloneFile(filepath.Join(*root, "images/base-rootfs-arm64.ext4"), disk))

	spec := vmm.Spec{
		ID: "spike", Dir: w, Kernel: kernel, Initrd: initrd,
		Cmdline:  "console=hvc0 rdinit=/init sbx.root=/dev/vda quiet",
		RootDisk: disk, VCPU: 2, MemMB: 1024, ConsoleLog: filepath.Join(w, "console.log"),
	}
	t0 := time.Now()
	m, err := d.Boot(ctx, spec)
	must(err)
	c, err := guestclient.Dial(func(ctx context.Context) (netConn, error) { return m.DialVsock(ctx, vmm.GuestPort) })
	must(err)
	wctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	_, err = c.WaitReady(wctx)
	cancel()
	if err != nil {
		b, _ := os.ReadFile(spec.ConsoleLog)
		log.Fatalf("not ready: %v\nconsole:\n%s", err, b)
	}
	log.Printf("cold boot to agent ready: %s", time.Since(t0))
	v, err := c.CheckVersion(ctx)
	must(err)
	log.Printf("guest: agent=%s kernel=%s arch=%s", v.AgentVersion, v.KernelRelease, v.Arch)
	run(ctx, c, "e1", "id; uname -a; node --version; python3 --version; df -h / | tail -1; nproc; free -m | head -2")

	must(c.Refresh(ctx))
	run(ctx, c, "e2", "sync; echo before-snapshot > /tmp/marker")

	t1 := time.Now()
	must(m.Pause(ctx))
	snapDir := filepath.Join(w, "snap")
	sf, err := m.Snapshot(ctx, snapDir)
	if err != nil {
		log.Printf("SNAPSHOT FAILED: %v", err)
		_ = m.Kill()
		return
	}
	log.Printf("snapshot: %s (state %d MB)", time.Since(t1), sf.MemBytes>>20)
	c.Close()
	must(m.Kill())

	for i := 0; i < 2; i++ {
		cs := spec
		cs.ID = fmt.Sprintf("clone%d", i)
		cs.RootDisk = filepath.Join(w, fmt.Sprintf("clone%d.img", i))
		cs.ConsoleLog = filepath.Join(w, fmt.Sprintf("clone%d.log", i))
		must(d.CloneFile(sf.Disk, cs.RootDisk))
		t2 := time.Now()
		cm, err := d.Restore(ctx, sf, cs)
		must(err)
		must(cm.Resume(ctx))
		cc, err := guestclient.Dial(func(ctx context.Context) (netConn, error) { return cm.DialVsock(ctx, vmm.GuestPort) })
		must(err)
		wctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		_, err = cc.WaitReady(wctx)
		cancel()
		must(err)
		must(cc.Refresh(ctx))
		log.Printf("clone%d restore to ready: %s", i, time.Since(t2))
		run(ctx, cc, fmt.Sprintf("c%d", i), fmt.Sprintf("cat /tmp/marker; head -c 8 /dev/urandom | od -An -tx1; echo clone%d > /tmp/who; cat /tmp/who", i))
		defer cm.Kill()
		defer cc.Close()
	}
	log.Printf("spike OK")
}
