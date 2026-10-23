package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	guestv1 "github.com/karthikponna/tryboxi/gen/guest/v1"
	"github.com/karthikponna/tryboxi/internal/network/policy"
	"github.com/karthikponna/tryboxi/internal/store/db"
	"github.com/karthikponna/tryboxi/internal/telemetry"
	"github.com/karthikponna/tryboxi/internal/vmm"
	"github.com/karthikponna/tryboxi/pkg/client"
)

const rescueScript = `set -e
mkdir -p /mnt/lost /tmp/recovery/files
# The device is a private clone, so ext4 can replay its journal (recent
# writes may only be in the journal after a crash).
mount -o ro /dev/vdb /mnt/lost || mount -o ro,noload /dev/vdb /mnt/lost
cd /mnt/lost/workspace
if [ -d .git ]; then
  git -c safe.directory='*' diff --binary HEAD > /tmp/recovery/workspace.patch 2>/dev/null || true
  git -c safe.directory='*' rev-parse HEAD > /tmp/recovery/HEAD 2>/dev/null || true
  git -c safe.directory='*' ls-files --others --exclude-standard -z > /tmp/recovery/untracked.list 2>/dev/null || true
  if [ -s /tmp/recovery/untracked.list ]; then
    tar --null -T /tmp/recovery/untracked.list -cf - 2>/dev/null | tar -xf - -C /tmp/recovery/files || true
  fi
else
  tar --exclude=node_modules --exclude=.venv -cf - . | tar -xf - -C /tmp/recovery/files
fi
echo "recovered from $(hostname) at $(date -u +%FT%TZ)" > /tmp/recovery/README
umount /mnt/lost || true
`

// Recover extracts the workspace of a lost sandbox into a "recovery"
// artifact using a rescue VM. The rescue VM gets a copy-on-write clone of
// the lost disk (the original is never modified) and the disk is never
// mounted on the host.
func (m *Manager) Recover(ctx context.Context, id string) (client.Artifact, error) {
	ctx, span := telemetry.Start(ctx, "sandbox.recover", telemetry.Sandbox(id))
	defer span.End()
	b, err := m.box(id)
	if err != nil {
		return client.Artifact{}, err
	}
	if st := b.State(); st != StateLost && st != StateFailed && st != StatePaused {
		return client.Artifact{}, errState("recover is for lost, failed, or paused sandboxes; %s is %s", id, st)
	}
	disk := filepath.Join(b.row.DirPath, "disk.img")
	if _, err := os.Stat(disk); err != nil {
		snaps, _ := m.st.R.ListSnapshots(ctx, db.ListSnapshotsParams{SourceSandboxID: id})
		if len(snaps) == 0 {
			return client.Artifact{}, errState("no disk image left for %s", id)
		}
		disk = snaps[0].DiskPath
	}
	base, err := m.ensureBaseTemplate(ctx)
	if err != nil {
		return client.Artifact{}, err
	}
	d := m.cfg.Defaults
	rb := m.builderBox(d.VCPU, d.MemMB, d.DiskMB, policy.Spec{Mode: policy.ModeNone}, "")
	defer m.discardBuilder(rb)
	if err := m.reserveBuilder(ctx, rb); err != nil {
		return client.Artifact{}, err
	}
	lostCopy := filepath.Join(rb.row.DirPath, "lost.img")
	if err := m.drv.CloneFile(disk, lostCopy); err != nil {
		return client.Artifact{}, err
	}
	if err := m.coldBootWith(ctx, rb, base.RootfsPath, d.DiskMB, []vmm.Disk{{Path: lostCopy}}); err != nil {
		return client.Artifact{}, fmt.Errorf("rescue VM: %w", err)
	}
	gc := rb.guest()
	var out strings.Builder
	st, err := gc.Exec(ctx, &guestv1.ExecRequest{ExecId: "rescue-" + id, Command: rescueScript, User: "root", TimeoutS: 300})
	if err != nil {
		return client.Artifact{}, err
	}
	for {
		f, err := st.Recv()
		if err != nil {
			return client.Artifact{}, fmt.Errorf("rescue script: %w", err)
		}
		if o := f.GetOutput(); o != nil {
			out.Write(o.Data)
		}
		if e := f.GetExit(); e != nil {
			if e.ExitCode != 0 {
				return client.Artifact{}, fmt.Errorf("rescue script exited %d: %s", e.ExitCode, truncateStr(out.String(), 1000))
			}
			break
		}
	}
	raw, err := getTar(ctx, gc, &guestv1.GetFilesRequest{Path: "/tmp/recovery", MaxBytes: 512 << 20})
	if err != nil {
		return client.Artifact{}, err
	}
	art, err := m.newArtifact(ctx, id, "", "recovery", ".tar", raw, map[string]string{"source_disk": filepath.Base(disk)},
		time.Duration(m.cfg.Retention.ArtifactsDays)*24*time.Hour)
	if err != nil {
		return client.Artifact{}, err
	}
	return client.Artifact{ID: art.ID, SandboxID: id, Kind: art.Kind, SizeBytes: art.SizeBytes, CreatedAt: art.CreatedAt}, nil
}

// coldBootWith is coldBoot with extra block devices.
func (m *Manager) coldBootWith(ctx context.Context, b *Box, rootfs string, diskMB int, extra []vmm.Disk) error {
	r := b.row
	disk := filepath.Join(r.DirPath, "disk.img")
	if err := m.drv.CloneFile(rootfs, disk); err != nil {
		return err
	}
	if fi, err := os.Stat(disk); err == nil && fi.Size() < int64(diskMB)<<20 {
		_ = os.Truncate(disk, int64(diskMB)<<20)
	}
	nw, err := m.newNet(b, false)
	if err != nil {
		return err
	}
	spec := m.baseSpec(b, disk, nw)
	spec.ExtraDisks = extra
	vm, err := m.drv.Boot(ctx, spec)
	if err != nil {
		nw.Close()
		return err
	}
	gc, err := m.dialGuest(vm)
	if err != nil {
		_ = vm.Kill()
		nw.Close()
		return err
	}
	b.mu.Lock()
	b.vm, b.net, b.gc = vm, nw, gc
	b.mu.Unlock()
	return m.waitGuest(ctx, vm, gc, 60*time.Second)
}
