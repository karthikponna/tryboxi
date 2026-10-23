package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/karthikponna/tryboxi/pkg/client"
)

// readSecretValue takes the value from an env var, stdin, or a hidden prompt.
// There is deliberately no --value flag: it would land in shell history.
func readSecretValue(fromEnv string, stdin bool, prompt string) (string, error) {
	switch {
	case fromEnv != "":
		v := os.Getenv(fromEnv)
		if v == "" {
			return "", fmt.Errorf("environment variable %s is empty", fromEnv)
		}
		return v, nil
	case stdin:
		b, err := io.ReadAll(bufio.NewReader(os.Stdin))
		return strings.TrimSpace(string(b)), err
	case term.IsTerminal(int(os.Stdin.Fd())):
		fmt.Fprint(os.Stderr, prompt)
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		return strings.TrimSpace(string(b)), err
	}
	return "", errors.New("provide the value with --from-env NAME or --stdin")
}

func secretCmd() *cobra.Command {
	sec := &cobra.Command{
		Use:   "secret",
		Short: "Host-side secrets: sandboxes get placeholders, the proxy injects real values",
		Long: `Secrets never enter sandboxes. A sandbox bound to a secret sees an env var
holding a random placeholder; the egress proxy replaces the placeholder with
the real value only on HTTPS requests to the secret's hosts, and blocks it
anywhere else. Git secrets let git inside the sandbox fetch (and optionally
push to allowed branches) without the token ever being in the VM.`,
	}

	var hosts []string
	var fromEnv string
	var stdin bool
	set := &cobra.Command{
		Use:   "set NAME --host api.example.com [--from-env VAR | --stdin]",
		Short: "Store an API key/token that may only be sent to the given hosts",
		Example: `  sandboxctl secret set OPENAI_API_KEY --host api.openai.com --from-env OPENAI_API_KEY
  sandboxctl secret set NPM_TOKEN --host registry.npmjs.org --stdin < token.txt`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			v, err := readSecretValue(fromEnv, stdin, "value for "+args[0]+": ")
			if err != nil {
				return err
			}
			cx, cancel := ctx()
			defer cancel()
			info, err := cli().PutSecret(cx, client.SecretRequest{Name: args[0], Kind: "env", Value: v, Hosts: hosts})
			if err != nil {
				return err
			}
			fmt.Printf("stored %s (%s) for %s\n", info.Name, info.Preview, strings.Join(info.Hosts, ", "))
			fmt.Printf("bind it: sandboxctl create --secret %s ...   or MCP env SANDBOX_SECRETS=%s\n", info.Name, info.Name)
			return nil
		},
	}
	set.Flags().StringSliceVar(&hosts, "host", nil, "host(s) allowed to receive the secret (repeatable, *.example.com ok)")
	set.Flags().StringVar(&fromEnv, "from-env", "", "read the value from this environment variable")
	set.Flags().BoolVar(&stdin, "stdin", false, "read the value from stdin")

	var repos, push []string
	var gitHosts []string
	var gFromEnv string
	var gStdin bool
	setGit := &cobra.Command{
		Use:   "set-git NAME --repo owner/name [--push 'sbx/*']",
		Short: "Store a git token used by the git proxy (fetch; push only to allowed branches)",
		Example: `  sandboxctl secret set-git GITHUB --repo acme/app --push 'sbx/*' --from-env GH_TOKEN
  sandboxctl secret set-git GITHUB_RO --repo 'acme/*' --from-env GH_TOKEN      # read-only`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			v, err := readSecretValue(gFromEnv, gStdin, "token for "+args[0]+": ")
			if err != nil {
				return err
			}
			cx, cancel := ctx()
			defer cancel()
			info, err := cli().PutSecret(cx, client.SecretRequest{Name: args[0], Kind: "git", Value: v, Hosts: gitHosts, Repos: repos, PushBranches: push})
			if err != nil {
				return err
			}
			mode := "read-only"
			if len(info.PushBranches) > 0 {
				mode = "push to " + strings.Join(info.PushBranches, ", ")
			}
			fmt.Printf("stored git secret %s for %s on %s (%s)\n", info.Name, strings.Join(info.Repos, ", "), strings.Join(info.Hosts, ", "), mode)
			return nil
		},
	}
	setGit.Flags().StringSliceVar(&repos, "repo", nil, "owner/name (globs ok: acme/*) the token may be used for")
	setGit.Flags().StringSliceVar(&push, "push", nil, "branch globs a push may update (omit for read-only)")
	setGit.Flags().StringSliceVar(&gitHosts, "host", []string{"github.com"}, "git host(s)")
	setGit.Flags().StringVar(&gFromEnv, "from-env", "", "read the token from this environment variable")
	setGit.Flags().BoolVar(&gStdin, "stdin", false, "read the token from stdin")

	ls := &cobra.Command{
		Use: "ls", Short: "List secrets (values are never shown)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cx, cancel := ctx()
			defer cancel()
			list, err := cli().Secrets(cx)
			if err != nil {
				return err
			}
			if flagJSON {
				printJSON(list)
				return nil
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "NAME\tKIND\tHOSTS\tREPOS\tPUSH\tVALUE\tUPDATED")
			for _, s := range list {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", s.Name, s.Kind, strings.Join(s.Hosts, ","), strings.Join(s.Repos, ","),
					strings.Join(s.PushBranches, ","), s.Preview, time.UnixMilli(s.UpdatedAt).Format("2006-01-02 15:04"))
			}
			return tw.Flush()
		},
	}
	rm := &cobra.Command{
		Use: "rm NAME", Short: "Delete a secret", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cx, cancel := ctx()
			defer cancel()
			if err := cli().DeleteSecret(cx, args[0]); err != nil {
				return err
			}
			fmt.Println("deleted", args[0])
			return nil
		},
	}
	sec.AddCommand(set, setGit, ls, rm)
	return sec
}

func traceCmd() *cobra.Command {
	var limit int
	c := &cobra.Command{
		Use:   "trace SANDBOX_OR_EXEC_ID",
		Short: "Show recent traces (per-phase timing waterfall) for a sandbox or execution",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cx, cancel := ctx()
			defer cancel()
			sbx, exe := args[0], ""
			if strings.HasPrefix(args[0], "exe_") {
				sbx, exe = "", args[0]
			}
			traces, err := cli().Traces(cx, sbx, exe, limit)
			if err != nil {
				return err
			}
			if flagJSON {
				printJSON(traces)
				return nil
			}
			for i := len(traces) - 1; i >= 0; i-- {
				t := traces[i]
				fmt.Printf("\n%s  %s  %.1fms  trace=%s\n", t.Start.Local().Format("15:04:05.000"), t.Root, t.DurMs, t.TraceID[:12])
				depth := map[string]int{}
				for _, s := range t.Spans {
					d := 0
					if s.ParentID != "" {
						d = depth[s.ParentID] + 1
					}
					depth[s.SpanID] = d
					off := float64(s.Start.Sub(t.Start).Microseconds()) / 1000
					bar := strings.Repeat(" ", min(40, int(off/max(t.DurMs, 1)*40))) + strings.Repeat("█", max(1, int(s.DurMs/max(t.DurMs, 1)*40)))
					mark := ""
					if s.Status == "error" {
						mark = "  ERROR " + s.Error
					}
					fmt.Printf("  %-42s %9.1fms  %-41s%s\n", strings.Repeat("  ", d)+s.Name, s.DurMs, bar, mark)
				}
			}
			return nil
		},
	}
	c.Flags().IntVarP(&limit, "limit", "n", 5, "number of traces")
	return c
}
