// Command sandboxctl is the CLI for sandboxd.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/karthikponna/tryboxi/pkg/client"
)

var (
	flagJSON   bool
	flagSocket string
	flagURL    string
)

func main() {
	root := &cobra.Command{Use: "sandboxctl", Short: "Control local microVM sandboxes (sandboxd)", SilenceUsage: true}
	root.PersistentFlags().BoolVar(&flagJSON, "json", false, "print raw JSON")
	root.PersistentFlags().StringVar(&flagSocket, "socket", "", "daemon socket (default: per-OS location)")
	root.PersistentFlags().StringVar(&flagURL, "url", "", "daemon URL for TCP access (uses SANDBOXD_TOKEN)")
	root.AddCommand(
		psCmd(), createCmd(), execCmd(), logsCmd(), grepCmd(), shellCmd(), rmCmd(), pauseCmd(), resumeCmd(),
		syncCmd(), cpCmd(), diffCmd(), applyCmd(), pullCmd(), undoCmd(), snapshotCmd(), forkCmd(), restoreCmd(),
		portCmd(), recoverCmd(), templatesCmd(), topCmd(), eventsCmd(), gcCmd(), healthCmd(), mcpCmd(), artifactsCmd(),
		secretCmd(), traceCmd(), netCmd(),
	)
	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

func cli() *client.Client {
	c, err := client.New(client.Options{Socket: flagSocket, URL: flagURL, Name: "sandboxctl"})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	return c
}

func ctx() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

func printJSON(v any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func ago(ms int64) string {
	if ms == 0 {
		return "-"
	}
	d := time.Since(time.UnixMilli(ms)).Round(time.Second)
	if d < time.Minute {
		return d.String() + " ago"
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh%dm ago", int(d.Hours()), int(d.Minutes())%60)
}

func psCmd() *cobra.Command {
	var all bool
	c := &cobra.Command{
		Use: "ps", Short: "List sandboxes",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cx, cancel := ctx()
			defer cancel()
			q := url.Values{}
			if all {
				q.Set("all", "1")
			}
			list, err := cli().ListSandboxes(cx, q)
			if err != nil {
				return err
			}
			if flagJSON {
				printJSON(list)
				return nil
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tSTATE\tHEALTH\tSIZE\tNET\tWORKSPACE\tEXECS\tACTIVE")
			for _, s := range list {
				ws := s.WorkspaceMode
				if s.HostPath != "" {
					ws += ":" + filepath.Base(s.HostPath)
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%dc/%dM\t%s\t%s\t%d\t%s\n", s.ID, s.State, s.Health, s.VCPU, s.MemMB, s.NetworkMode, ws, s.ExecCount, ago(s.LastActiveAt))
			}
			return tw.Flush()
		},
	}
	c.Flags().BoolVarP(&all, "all", "a", false, "include destroyed")
	return c
}

func createCmd() *cobra.Command {
	var req client.CreateSandboxRequest
	var workspace, mode, net, setupMode string
	var allow, autoPull []string
	c := &cobra.Command{
		Use: "create", Short: "Create a sandbox",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cx, cancel := ctx()
			defer cancel()
			if workspace != "" {
				abs, _ := filepath.Abs(workspace)
				req.Workspace = &client.WorkspaceSpec{HostPath: abs, Mode: mode, AutoPull: autoPull}
			}
			if net != "" || len(allow) > 0 || setupMode != "" {
				req.Network = &client.NetworkSpec{Mode: net, Allow: allow, SetupMode: setupMode}
			}
			s, err := cli().CreateSandbox(cx, req)
			if err != nil {
				return err
			}
			if flagJSON {
				printJSON(s)
				return nil
			}
			fmt.Printf("%s  %s (booted from %s in %dms)\n", s.ID, s.State, s.BootedFrom, s.BootMs)
			for _, st := range s.Setup {
				code := "?"
				if st.ExitCode != nil {
					code = strconv.Itoa(*st.ExitCode)
				}
				fmt.Printf("  setup: %-40s exit=%s (%s)\n", st.Cmd, code, st.ExecID)
			}
			if s.Notice != "" {
				fmt.Println("note:", s.Notice)
			}
			return nil
		},
	}
	f := c.Flags()
	f.StringVarP(&workspace, "workspace", "w", "", "host repo to sync into /workspace")
	f.StringVar(&mode, "mode", "", "workspace mode: host | sandbox")
	f.StringVarP(&req.Template, "template", "t", "", `"base", "auto", or a template name`)
	f.IntVar(&req.VCPU, "cpus", 0, "vCPUs")
	f.IntVar(&req.MemMB, "mem", 0, "memory MB")
	f.IntVar(&req.DiskMB, "disk", 0, "disk MB")
	f.StringVar(&net, "net", "", "network mode: none | allowlist | full")
	f.StringSliceVar(&allow, "allow", nil, "extra allowed hosts, CIDRs, or @presets (@browsers, @huggingface, ...)")
	f.StringArrayVar(&req.Setup, "setup", nil, "setup command run once with open network, then the sandbox is locked down (repeatable)")
	f.StringVar(&setupMode, "setup-net", "", "network mode during setup (default full)")
	f.StringSliceVar(&autoPull, "auto-pull", nil, `files copied back to the host when commands change them ("default" = manifests + lockfiles)`)
	f.StringVar(&req.SessionKey, "session", "", "session key (reuse a live sandbox)")
	f.StringSliceVar(&req.Secrets, "secret", nil, "bind a stored secret by name (VM sees a placeholder only)")
	f.IntVar(&req.IdleTimeoutS, "idle", 0, "idle timeout seconds before pause-to-disk")
	return c
}

func execCmd() *cobra.Command {
	var timeout int
	var bg, noWait, raw bool
	var cwd string
	c := &cobra.Command{
		Use: "exec SANDBOX -- COMMAND...", Short: "Run a command (streams output)", Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cx, cancel := ctx()
			defer cancel()
			cl := cli()
			req := client.ExecRequest{Cmd: strings.Join(args[1:], " "), TimeoutS: timeout, Background: bg, Cwd: cwd, Wait: client.Bool(false)}
			e, err := cl.Exec(cx, args[0], req)
			if err != nil {
				return err
			}
			if bg || noWait {
				fmt.Println(e.ID)
				return nil
			}
			var final *client.Execution
			err = cl.Stream(cx, e.ID, 0, func(f client.StreamFrame) error {
				switch f.Stream {
				case "stdout":
					fmt.Fprint(os.Stdout, f.Data)
				case "stderr":
					fmt.Fprint(os.Stderr, f.Data)
				case "exit":
					final = f.Exit
				}
				return nil
			})
			if err != nil {
				return err
			}
			if final == nil {
				ex, err := cl.GetExecution(context.Background(), e.ID)
				if err != nil {
					return err
				}
				final = &ex
			}
			if flagJSON {
				printJSON(final)
			} else if !raw {
				code := "?"
				if final.ExitCode != nil {
					code = strconv.Itoa(*final.ExitCode)
				}
				fmt.Fprintf(os.Stderr, "[%s exit=%s %s %dms", final.ID, code, final.Status, final.DurationMs)
				if len(final.ChangedFiles) > 0 {
					fmt.Fprintf(os.Stderr, " changed=%s", strings.Join(final.ChangedFiles, ","))
				}
				fmt.Fprintln(os.Stderr, "]")
			}
			if final.ExitCode != nil && *final.ExitCode != 0 {
				os.Exit(*final.ExitCode)
			}
			return nil
		},
	}
	c.Flags().IntVar(&timeout, "timeout", 0, "timeout seconds")
	c.Flags().BoolVarP(&bg, "background", "d", false, "run in background (servers)")
	c.Flags().BoolVar(&noWait, "no-wait", false, "print the exec id and return")
	c.Flags().BoolVar(&raw, "raw", false, "no summary line")
	c.Flags().StringVar(&cwd, "cwd", "", "working directory (default /workspace)")
	return c
}

func logsCmd() *cobra.Command {
	var stream string
	var follow bool
	c := &cobra.Command{
		Use: "logs EXEC_ID", Short: "Print an execution's output", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cx, cancel := ctx()
			defer cancel()
			cl := cli()
			if follow {
				return cl.Stream(cx, args[0], 0, func(f client.StreamFrame) error {
					if f.Stream == "stdout" || (f.Stream == "stderr" && stream != "stdout") {
						fmt.Print(f.Data)
					}
					return nil
				})
			}
			var off int64
			for {
				ch, err := cl.Output(cx, args[0], stream, off, 1<<20)
				if err != nil {
					return err
				}
				fmt.Print(ch.Data)
				off += int64(len(ch.Data))
				if ch.EOF || len(ch.Data) == 0 {
					return nil
				}
			}
		},
	}
	c.Flags().StringVar(&stream, "stream", "stdout", "stdout | stderr")
	c.Flags().BoolVarP(&follow, "follow", "f", false, "follow live output")
	return c
}

func grepCmd() *cobra.Command {
	return &cobra.Command{
		Use: "grep EXEC_ID PATTERN", Short: "Search an execution's output", Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cx, cancel := ctx()
			defer cancel()
			res, err := cli().Grep(cx, args[0], args[1], "")
			if err != nil {
				return err
			}
			for _, m := range res.Matches {
				fmt.Printf("%d: %s\n", m.Line, m.Text)
			}
			return nil
		},
	}
}

func simpleSandboxCmd(use, short string, fn func(context.Context, *client.Client, string) (any, error)) *cobra.Command {
	return &cobra.Command{
		Use: use + " SANDBOX", Short: short, Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cx, cancel := ctx()
			defer cancel()
			v, err := fn(cx, cli(), args[0])
			if err != nil {
				return err
			}
			printJSON(v)
			return nil
		},
	}
}

func rmCmd() *cobra.Command {
	return &cobra.Command{
		Use: "rm SANDBOX...", Short: "Destroy sandboxes", Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cx, cancel := ctx()
			defer cancel()
			cl := cli()
			for _, id := range args {
				if err := cl.DestroySandbox(cx, id); err != nil {
					return err
				}
				fmt.Println("destroyed", id)
			}
			return nil
		},
	}
}

func pauseCmd() *cobra.Command {
	return simpleSandboxCmd("pause", "Pause a sandbox to disk", func(c context.Context, cl *client.Client, id string) (any, error) { return cl.Pause(c, id) })
}

func resumeCmd() *cobra.Command {
	return simpleSandboxCmd("resume", "Resume a paused sandbox", func(c context.Context, cl *client.Client, id string) (any, error) { return cl.Resume(c, id) })
}

func syncCmd() *cobra.Command {
	return simpleSandboxCmd("sync", "Push host workspace changes into the sandbox", func(c context.Context, cl *client.Client, id string) (any, error) { return cl.Sync(c, id) })
}

func diffCmd() *cobra.Command {
	return &cobra.Command{
		Use: "diff SANDBOX", Short: "Show the sandbox's workspace diff", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cx, cancel := ctx()
			defer cancel()
			d, err := cli().Diff(cx, args[0])
			if err != nil {
				return err
			}
			if flagJSON {
				printJSON(d)
				return nil
			}
			fmt.Printf("artifact %s (base %s)\n", d.ArtifactID, shortSHA(d.Head))
			for _, c := range d.Changes {
				fmt.Printf("  %s %s\n", c.Status, c.Path)
			}
			fmt.Print(d.Patch)
			return nil
		},
	}
}

func shortSHA(s string) string {
	if len(s) > 10 {
		return s[:10]
	}
	return s
}

func printApply(r client.ApplyResult) {
	if flagJSON {
		printJSON(r)
		return
	}
	for _, f := range r.Files {
		line := fmt.Sprintf("  %-9s %s", f.Action, f.Path)
		if f.Reason != "" {
			line += "  (" + f.Reason + ")"
		}
		fmt.Println(line)
	}
	fmt.Printf("applied=%d conflicts=%d skipped=%d\n", r.Applied, r.Conflicts, r.Skipped)
	if r.BackupArtifactID != "" {
		fmt.Printf("undo with: sandboxctl undo %s\n", r.BackupArtifactID)
	}
}

func applyCmd() *cobra.Command {
	var artifact string
	c := &cobra.Command{
		Use: "apply SANDBOX [PATH...]", Short: "Merge sandbox changes into the host repo (backup + undo)", Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cx, cancel := ctx()
			defer cancel()
			r, err := cli().Apply(cx, args[0], client.ApplyRequest{ArtifactID: artifact, Paths: args[1:]})
			if err != nil {
				return err
			}
			printApply(r)
			return nil
		},
	}
	c.Flags().StringVar(&artifact, "artifact", "", "diff artifact (default: a fresh diff)")
	return c
}

func pullCmd() *cobra.Command {
	var force bool
	c := &cobra.Command{
		Use: "pull SANDBOX [PATH...]", Short: "Copy files changed in the sandbox to the host (backup + undo)", Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cx, cancel := ctx()
			defer cancel()
			r, err := cli().Pull(cx, args[0], client.PullRequest{Paths: args[1:], Force: force})
			if err != nil {
				return err
			}
			printApply(r)
			return nil
		},
	}
	c.Flags().BoolVar(&force, "force", false, "overwrite host files changed since the last sync")
	return c
}

func undoCmd() *cobra.Command {
	return &cobra.Command{
		Use: "undo BACKUP_ARTIFACT", Short: "Restore host files from a backup", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cx, cancel := ctx()
			defer cancel()
			r, err := cli().Undo(cx, args[0])
			if err != nil {
				return err
			}
			fmt.Printf("restored %d file(s), removed %d created file(s)\n", r.Restored, r.Removed)
			return nil
		},
	}
}

func cpCmd() *cobra.Command {
	return &cobra.Command{
		Use: "cp SRC DST", Short: "Copy a file: local -> SANDBOX:/path or SANDBOX:/path -> local", Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cx, cancel := ctx()
			defer cancel()
			cl := cli()
			src, dst := args[0], args[1]
			if id, p, ok := strings.Cut(dst, ":"); ok && strings.HasPrefix(id, "sbx_") {
				data, err := os.ReadFile(src)
				if err != nil {
					return err
				}
				return cl.WriteFile(cx, id, p, data)
			}
			if id, p, ok := strings.Cut(src, ":"); ok && strings.HasPrefix(id, "sbx_") {
				data, err := cl.ReadFile(cx, id, p)
				if err != nil {
					return err
				}
				if dst == "-" {
					_, err = os.Stdout.Write(data)
					return err
				}
				return os.WriteFile(dst, data, 0o644)
			}
			return fmt.Errorf("one side must be SANDBOX:/path")
		},
	}
}

func snapshotCmd() *cobra.Command {
	var label string
	c := &cobra.Command{
		Use: "snapshot [SANDBOX]", Short: "Checkpoint a sandbox, or list snapshots", Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cx, cancel := ctx()
			defer cancel()
			cl := cli()
			if len(args) == 1 && cmd.Flags().Changed("label") || len(args) == 1 && !cmd.Flags().Changed("list") {
				s, err := cl.Checkpoint(cx, args[0], label)
				if err != nil {
					return err
				}
				fmt.Printf("%s  checkpoint of %s (%d MB)\n", s.ID, s.SandboxID, s.SizeBytes>>20)
				return nil
			}
			id := ""
			if len(args) == 1 {
				id = args[0]
			}
			list, err := cl.ListSnapshots(cx, id)
			if err != nil {
				return err
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tKIND\tSANDBOX\tLABEL\tSIZE\tCREATED")
			for _, s := range list {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%dM\t%s\n", s.ID, s.Kind, s.SandboxID, s.Label, s.SizeBytes>>20, ago(s.CreatedAt))
			}
			return tw.Flush()
		},
	}
	c.Flags().StringVar(&label, "label", "", "checkpoint label")
	c.Flags().Bool("list", false, "list snapshots instead of creating one")
	return c
}

func forkCmd() *cobra.Command {
	var n int
	c := &cobra.Command{
		Use: "fork SANDBOX", Short: "Clone a running sandbox N times", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cx, cancel := ctx()
			defer cancel()
			r, err := cli().Fork(cx, args[0], n)
			if err != nil {
				return err
			}
			if flagJSON {
				printJSON(r)
				return nil
			}
			for _, s := range r.Sandboxes {
				fmt.Printf("%s  %s (%dms)\n", s.ID, s.State, s.BootMs)
			}
			for _, f := range r.Failed {
				fmt.Fprintln(os.Stderr, "clone failed:", f)
			}
			return nil
		},
	}
	c.Flags().IntVarP(&n, "count", "n", 2, "number of clones")
	return c
}

func restoreCmd() *cobra.Command {
	var snap string
	c := &cobra.Command{
		Use: "restore SANDBOX", Short: "New sandbox from a snapshot (default: latest checkpoint)", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cx, cancel := ctx()
			defer cancel()
			s, err := cli().Restore(cx, args[0], snap)
			if err != nil {
				return err
			}
			fmt.Printf("%s  %s (from %s)\n", s.ID, s.State, args[0])
			return nil
		},
	}
	c.Flags().StringVar(&snap, "snapshot", "latest", "snapshot id")
	return c
}

func portCmd() *cobra.Command {
	return &cobra.Command{
		Use: "port SANDBOX GUEST_PORT", Short: "Expose a guest port on 127.0.0.1", Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cx, cancel := ctx()
			defer cancel()
			p, _ := strconv.Atoi(args[1])
			r, err := cli().ForwardPort(cx, args[0], p)
			if err != nil {
				return err
			}
			fmt.Println(r.URL)
			return nil
		},
	}
}

func recoverCmd() *cobra.Command {
	var out string
	c := &cobra.Command{
		Use: "recover SANDBOX", Short: "Extract a lost sandbox's workspace into a tar", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cx, cancel := ctx()
			defer cancel()
			cl := cli()
			a, err := cl.Recover(cx, args[0])
			if err != nil {
				return err
			}
			fmt.Printf("recovery artifact %s (%d bytes)\n", a.ID, a.SizeBytes)
			if out != "" {
				b, err := cl.ArtifactContent(cx, a.ID)
				if err != nil {
					return err
				}
				if err := os.WriteFile(out, b, 0o600); err != nil {
					return err
				}
				fmt.Println("wrote", out)
			}
			return nil
		},
	}
	c.Flags().StringVarP(&out, "output", "o", "", "write the tar here")
	return c
}

func templatesCmd() *cobra.Command {
	var build string
	var wait bool
	c := &cobra.Command{
		Use: "templates", Short: "List templates, or --build REPO to prebuild a repo template",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cx, cancel := ctx()
			defer cancel()
			cl := cli()
			if build != "" {
				abs, _ := filepath.Abs(build)
				t, err := cl.CreateTemplate(cx, client.TemplateRequest{RepoPath: abs, Wait: wait})
				if err != nil {
					return err
				}
				printJSON(t)
				return nil
			}
			list, err := cl.Templates(cx)
			if err != nil {
				return err
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tNAME\tSTATUS\tSTACK\tBUILT")
			for _, t := range list {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", t.ID, t.Name, t.Status, t.DetectedFrom, ago(t.BuiltAt))
			}
			return tw.Flush()
		},
	}
	c.Flags().StringVar(&build, "build", "", "repo path to build a dependency-preinstalled template for")
	c.Flags().BoolVar(&wait, "wait", false, "wait for the build")
	return c
}

func topCmd() *cobra.Command {
	return &cobra.Command{
		Use: "top", Short: "Show capacity and sandboxes (refreshes every 2s)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cx, cancel := ctx()
			defer cancel()
			cl := cli()
			for {
				capy, err := cl.Capacity(cx)
				if err != nil {
					return err
				}
				list, _ := cl.ListSandboxes(cx, nil)
				fmt.Print("\033[H\033[2J")
				fmt.Printf("sandboxd  vCPU %.0f/%.0f free  mem %d/%d MB free  sandboxes %d/%d  pool %d  queue %d  pressure %s\n\n",
					capy.VCPUFree, capy.VCPUTotal, capy.MemFreeMB, capy.MemTotalMB, capy.Sandboxes, capy.MaxSandbox, capy.PoolVMs, capy.QueueLen, capy.Pressure)
				sort.Slice(list, func(i, j int) bool { return list[i].LastActiveAt > list[j].LastActiveAt })
				tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
				fmt.Fprintln(tw, "ID\tSTATE\tHEALTH\tSIZE\tEXECS\tACTIVE")
				for _, s := range list {
					fmt.Fprintf(tw, "%s\t%s\t%s\t%dc/%dM\t%d\t%s\n", s.ID, s.State, s.Health, s.VCPU, s.MemMB, s.ExecCount, ago(s.LastActiveAt))
				}
				_ = tw.Flush()
				select {
				case <-cx.Done():
					return nil
				case <-time.After(2 * time.Second):
				}
			}
		},
	}
}

func eventsCmd() *cobra.Command {
	var sandbox string
	var after int64
	c := &cobra.Command{
		Use: "events", Short: "Follow the daemon event feed",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cx, cancel := ctx()
			defer cancel()
			return cli().Events(cx, sandbox, after, func(e client.Event) error {
				if flagJSON {
					b, _ := json.Marshal(e)
					fmt.Println(string(b))
					return nil
				}
				fmt.Printf("%s  %-22s %-30s %s\n", time.UnixMilli(e.Ts).Format("15:04:05.000"), e.Type, e.SandboxID, string(e.Data))
				return nil
			})
		},
	}
	c.Flags().StringVar(&sandbox, "sandbox", "", "only this sandbox")
	c.Flags().Int64Var(&after, "after", 0, "replay events after this id")
	return c
}

func gcCmd() *cobra.Command {
	var dry bool
	c := &cobra.Command{
		Use: "gc", Short: "Garbage-collect expired snapshots, logs, artifacts",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cx, cancel := ctx()
			defer cancel()
			r, err := cli().GC(cx, dry)
			if err != nil {
				return err
			}
			verb := "deleted"
			if r.DryRun {
				verb = "would delete"
			}
			for _, d := range r.Deleted {
				fmt.Printf("%s %s\n", verb, d)
			}
			fmt.Printf("%s %d item(s), %d MB\n", verb, len(r.Deleted), r.Bytes>>20)
			return nil
		},
	}
	c.Flags().BoolVar(&dry, "dry-run", false, "list without deleting")
	return c
}

func healthCmd() *cobra.Command {
	return &cobra.Command{
		Use: "doctor", Aliases: []string{"health"}, Short: "Check that the daemon is reachable and healthy",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cx, cancel := ctx()
			defer cancel()
			cl := cli()
			h, err := cl.Health(cx)
			if err != nil {
				fmt.Fprintln(os.Stderr, "daemon unreachable at", client.DefaultSocket())
				fmt.Fprintln(os.Stderr, "start it with `sandboxd serve` (foreground) or `sandboxd install --artifacts _out` (LaunchAgent), then run `sandboxd doctor` for host checks")
				return err
			}
			capy, _ := cl.Capacity(cx)
			printJSON(map[string]any{"health": h, "capacity": capy})
			return nil
		},
	}
}

func artifactsCmd() *cobra.Command {
	var sandbox, kind, out string
	c := &cobra.Command{
		Use: "artifacts [ARTIFACT_ID]", Short: "List artifacts, or download one with -o", Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cx, cancel := ctx()
			defer cancel()
			cl := cli()
			if len(args) == 1 {
				b, err := cl.ArtifactContent(cx, args[0])
				if err != nil {
					return err
				}
				if out == "" || out == "-" {
					_, err = os.Stdout.Write(b)
					return err
				}
				return os.WriteFile(out, b, 0o600)
			}
			list, err := cl.ListArtifacts(cx, sandbox, kind)
			if err != nil {
				return err
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tKIND\tSANDBOX\tSIZE\tCREATED")
			for _, a := range list {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\n", a.ID, a.Kind, a.SandboxID, a.SizeBytes, ago(a.CreatedAt))
			}
			return tw.Flush()
		},
	}
	c.Flags().StringVar(&sandbox, "sandbox", "", "filter by sandbox")
	c.Flags().StringVar(&kind, "kind", "", "diff | backup | tar | recovery")
	c.Flags().StringVarP(&out, "output", "o", "", "download destination")
	return c
}

var _ = io.EOF
