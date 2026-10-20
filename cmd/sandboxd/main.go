// Command sandboxd is the host daemon: it runs disposable Linux microVMs and
// serves the sandbox HTTP API on a unix socket.
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	apispec "github.com/karthikponna/tryboxi/api"
	"github.com/karthikponna/tryboxi/internal/api"
	"github.com/karthikponna/tryboxi/internal/api/middleware"
	"github.com/karthikponna/tryboxi/internal/backend"
	"github.com/karthikponna/tryboxi/internal/config"
	"github.com/karthikponna/tryboxi/internal/platform"
	"github.com/karthikponna/tryboxi/internal/safefs"
	"github.com/karthikponna/tryboxi/internal/service"
	"github.com/karthikponna/tryboxi/internal/store"
	"github.com/karthikponna/tryboxi/internal/version"
)

func main() {
	root := &cobra.Command{Use: "sandboxd", Short: "Local microVM sandboxes for AI agents", SilenceUsage: true}
	root.AddCommand(serveCmd(), installCmd(), uninstallCmd(), doctorCmd(), configCmd(), tokenCmd(), versionCmd())
	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

func loadConfig() (*config.Config, error) {
	return config.Load(config.Path(config.DefaultDataRoot()))
}

func newLogger(level string) *slog.Logger {
	var lv slog.Level
	_ = lv.UnmarshalText([]byte(level))
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lv}))
}

func serveCmd() *cobra.Command {
	var debug bool
	c := &cobra.Command{
		Use:   "serve",
		Short: "Run the daemon in the foreground",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			if debug {
				cfg.LogLevel = "debug"
			}
			log := newLogger(cfg.LogLevel)
			ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()

			st, err := store.Open(ctx, cfg.Dir("state.db"))
			if err != nil {
				return err
			}
			defer st.Close()
			be, err := backend.New(cfg.DataRoot, log)
			if err != nil {
				return err
			}
			mgr, err := service.New(context.Background(), cfg, st, be, log)
			if err != nil {
				return err
			}
			if err := mgr.Start(); err != nil {
				return err
			}
			tok, _, err := middleware.LoadOrCreateToken(cfg.Dir("token"))
			if err != nil {
				return err
			}
			srv := api.New(mgr, log, apispec.OpenAPI)
			hs := &http.Server{Handler: srv.Handler(tok), ConnContext: api.ConnContext, ReadHeaderTimeout: 10 * time.Second}

			if err := os.MkdirAll(filepath.Dir(cfg.Socket), 0o700); err != nil {
				return err
			}
			if fi, err := os.Lstat(cfg.Socket); err == nil && fi.Mode()&os.ModeSocket != 0 {
				if c, err := net.Dial("unix", cfg.Socket); err == nil {
					c.Close()
					return fmt.Errorf("another sandboxd is already listening on %s", cfg.Socket)
				}
				_ = os.Remove(cfg.Socket)
			}
			ul, err := net.Listen("unix", cfg.Socket)
			if err != nil {
				return err
			}
			mode := os.FileMode(0o600)
			if runtime.GOOS == "linux" && os.Geteuid() == 0 {
				mode = 0o660
			}
			_ = os.Chmod(cfg.Socket, mode)
			go func() { _ = hs.Serve(ul) }()
			log.Info("sandboxd listening", "socket", cfg.Socket, "data_root", cfg.DataRoot, "backend", be.Driver().Name(), "version", version.Version)
			if cfg.TCPListen != "" {
				tl, err := net.Listen("tcp", cfg.TCPListen)
				if err != nil {
					return err
				}
				go func() { _ = hs.Serve(tl) }()
				log.Info("sandboxd TCP listener (bearer token required)", "addr", cfg.TCPListen, "token_file", cfg.Dir("token"))
			}
			<-ctx.Done()
			log.Info("signal received, shutting down")
			sctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			_ = hs.Shutdown(sctx)
			mgr.Shutdown(sctx)
			_ = os.Remove(cfg.Socket)
			return nil
		},
	}
	c.Flags().BoolVar(&debug, "debug", false, "debug logging")
	return c
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

func installCmd() *cobra.Command {
	var artifacts string
	var noService bool
	c := &cobra.Command{
		Use:   "install",
		Short: "Install guest kernel, agent, and base rootfs; register the background service",
		Long: `Copies the guest kernel, sandbox-agent, and base rootfs from --artifacts into the
data root, writes a default config, and (on macOS) registers a per-user
LaunchAgent so the daemon starts at login. The base golden snapshot is built
the first time the daemon starts (a few seconds).`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			if artifacts == "" {
				return errors.New("--artifacts is required (directory containing images/ and bin/, e.g. _out)")
			}
			kdir := cfg.Dir("kernels")
			if err := os.MkdirAll(kdir, 0o700); err != nil {
				return err
			}
			_ = os.Chmod(cfg.DataRoot, 0o700)
			arch := runtime.GOARCH
			files := []struct{ src, dst string }{
				{filepath.Join(artifacts, "images", "vmlinux-"+arch), service.KernelFile()},
				{filepath.Join(artifacts, "bin", "sandbox-agent-linux-"+arch), service.AgentFile()},
			}
			for _, f := range files {
				if err := copyFile(f.src, filepath.Join(kdir, f.dst), 0o600); err != nil {
					return fmt.Errorf("copy %s: %w", f.src, err)
				}
				fmt.Printf("installed %s\n", filepath.Join(kdir, f.dst))
			}
			rootfs := filepath.Join(artifacts, "images", "base-rootfs-"+arch+".ext4")
			dst := filepath.Join(kdir, service.RootfsFile())
			_ = os.Remove(dst)
			if err := cloneOrCopy(rootfs, dst); err != nil {
				return fmt.Errorf("copy rootfs: %w", err)
			}
			if b, err := os.ReadFile(rootfs + ".sha256"); err == nil {
				_ = os.WriteFile(dst+".sha256", b, 0o600)
			}
			fmt.Printf("installed %s\n", dst)
			cfgPath := config.Path(cfg.DataRoot)
			if _, err := os.Stat(cfgPath); errors.Is(err, os.ErrNotExist) {
				b, _ := cfg.Marshal()
				_ = os.WriteFile(cfgPath, append([]byte("# sandboxd configuration; every field is optional.\n"), b...), 0o600)
				fmt.Printf("wrote %s\n", cfgPath)
			}
			if _, tok, err := middleware.LoadOrCreateToken(cfg.Dir("token")); err == nil && tok != "" {
				fmt.Printf("API token (for TCP or forwarded sockets) stored in %s\n", cfg.Dir("token"))
			}
			if !noService {
				if err := installService(cfg); err != nil {
					return err
				}
			} else {
				fmt.Println("skipped service registration; run `sandboxd serve` yourself")
			}
			return nil
		},
	}
	c.Flags().StringVar(&artifacts, "artifacts", "", "directory with images/vmlinux-<arch>, images/base-rootfs-<arch>.ext4, bin/sandbox-agent-linux-<arch>")
	c.Flags().BoolVar(&noService, "no-service", false, "do not register a LaunchAgent/systemd unit")
	return c
}

func cloneOrCopy(src, dst string) error {
	if runtime.GOOS == "darwin" {
		if err := exec.Command("cp", "-c", src, dst).Run(); err == nil {
			return nil
		}
	}
	return copyFile(src, dst, 0o600)
}

const launchAgentLabel = "com.tryboxi.sandboxd"

func launchAgentPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", launchAgentLabel+".plist")
}

func installService(cfg *config.Config) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	exe, _ = filepath.EvalSymlinks(exe)
	switch runtime.GOOS {
	case "darwin":
		plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key><string>%s</string>
	<key>ProgramArguments</key><array><string>%s</string><string>serve</string></array>
	<key>RunAtLoad</key><true/>
	<key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict>
	<key>ProcessType</key><string>Interactive</string>
	<key>StandardOutPath</key><string>%s</string>
	<key>StandardErrorPath</key><string>%s</string>
</dict>
</plist>
`, launchAgentLabel, exe, cfg.Dir("sandboxd.log"), cfg.Dir("sandboxd.log"))
		p := launchAgentPath()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, []byte(plist), 0o644); err != nil {
			return err
		}
		uid := fmt.Sprint(os.Getuid())
		_ = exec.Command("launchctl", "bootout", "gui/"+uid+"/"+launchAgentLabel).Run()
		if out, err := exec.Command("launchctl", "bootstrap", "gui/"+uid, p).CombinedOutput(); err != nil {
			return fmt.Errorf("launchctl bootstrap: %v: %s", err, out)
		}
		fmt.Printf("registered LaunchAgent %s (logs: %s)\n", p, cfg.Dir("sandboxd.log"))
	case "linux":
		unit := fmt.Sprintf(`[Unit]
Description=sandboxd microVM sandboxes
After=network-online.target

[Service]
ExecStart=%s serve
Restart=on-failure
KillMode=process
LimitNOFILE=1048576

[Install]
WantedBy=multi-user.target
`, exe)
		if os.Geteuid() != 0 {
			return errors.New("on Linux, install the service as root (sudo sandboxd install) or pass --no-service")
		}
		if err := os.WriteFile("/etc/systemd/system/sandboxd.service", []byte(unit), 0o644); err != nil {
			return err
		}
		_ = exec.Command("groupadd", "-f", "sandboxd").Run()
		for _, args := range [][]string{{"daemon-reload"}, {"enable", "--now", "sandboxd"}} {
			if out, err := exec.Command("systemctl", args...).CombinedOutput(); err != nil {
				return fmt.Errorf("systemctl %v: %v: %s", args, err, out)
			}
		}
		fmt.Println("installed and started sandboxd.service; add users to the 'sandboxd' group to grant access (equivalent to running VMs)")
	}
	return nil
}

func uninstallCmd() *cobra.Command {
	var purge, yes bool
	c := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove the background service (keeps all data unless --purge)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			switch runtime.GOOS {
			case "darwin":
				_ = exec.Command("launchctl", "bootout", fmt.Sprintf("gui/%d/%s", os.Getuid(), launchAgentLabel)).Run()
				if err := os.Remove(launchAgentPath()); err == nil {
					fmt.Println("removed", launchAgentPath())
				}
			case "linux":
				_ = exec.Command("systemctl", "disable", "--now", "sandboxd").Run()
				_ = os.Remove("/etc/systemd/system/sandboxd.service")
			}
			if !purge {
				fmt.Printf("kept all data in %s (pass --purge to delete it)\n", cfg.DataRoot)
				return nil
			}
			if !strings.Contains(filepath.Base(cfg.DataRoot), "sandboxd") {
				return fmt.Errorf("refusing to purge %s: data root name must contain 'sandboxd'", cfg.DataRoot)
			}
			if !yes {
				fmt.Printf("This permanently deletes %s (snapshots, logs, backups). Type the word 'purge' to confirm: ", cfg.DataRoot)
				line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
				if strings.TrimSpace(line) != "purge" {
					return errors.New("not confirmed; nothing deleted")
				}
			}
			if err := safefs.RemoveUnder(filepath.Dir(cfg.DataRoot), cfg.DataRoot); err != nil {
				return err
			}
			fmt.Println("deleted", cfg.DataRoot)
			return nil
		},
	}
	c.Flags().BoolVar(&purge, "purge", false, "also delete the data root")
	c.Flags().BoolVar(&yes, "yes", false, "skip the purge confirmation prompt")
	return c
}

func doctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check host support and installation",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			_ = os.MkdirAll(cfg.DataRoot, 0o700)
			checks := platform.Checks(cfg.DataRoot)
			for _, f := range []string{service.KernelFile(), service.AgentFile(), service.RootfsFile()} {
				_, err := os.Stat(cfg.Dir("kernels", f))
				checks = append(checks, platform.Check{Name: "artifact " + f, OK: err == nil, Fix: "sandboxd install --artifacts _out"})
			}
			conn, err := net.DialTimeout("unix", cfg.Socket, time.Second)
			if conn != nil {
				conn.Close()
			}
			checks = append(checks, platform.Check{Name: "daemon socket", OK: err == nil, Detail: cfg.Socket, Fix: "start the daemon: sandboxd serve (or sandboxd install)"})
			bad := 0
			for _, c := range checks {
				mark := "ok  "
				if !c.OK {
					mark = "FAIL"
					bad++
				}
				fmt.Printf("[%s] %-28s %s\n", mark, c.Name, c.Detail)
				if !c.OK && c.Fix != "" {
					fmt.Printf("       fix: %s\n", c.Fix)
				}
			}
			if bad > 0 {
				return fmt.Errorf("%d check(s) failed", bad)
			}
			return nil
		},
	}
}

func configCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "config",
		Short: "Print the effective configuration",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			b, err := cfg.Marshal()
			if err != nil {
				return err
			}
			fmt.Printf("# %s\n%s", config.Path(cfg.DataRoot), b)
			return nil
		},
	}
}

func tokenCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "token",
		Short: "Print the API bearer token (for TCP or forwarded-socket clients)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			_ = os.MkdirAll(cfg.DataRoot, 0o700)
			_, tok, err := middleware.LoadOrCreateToken(cfg.Dir("token"))
			if err != nil {
				return err
			}
			fmt.Println(tok)
			return nil
		},
	}
}

func versionCmd() *cobra.Command {
	return &cobra.Command{Use: "version", Run: func(*cobra.Command, []string) { fmt.Println("sandboxd", version.Version) }}
}
