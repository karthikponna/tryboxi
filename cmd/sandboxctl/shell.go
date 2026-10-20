package main

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/karthikponna/tryboxi/pkg/client"
)

func shellCmd() *cobra.Command {
	var user string
	c := &cobra.Command{
		Use: "shell SANDBOX", Short: "Interactive shell inside a sandbox", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cl := cli()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			rows, cols := 40, 120
			if w, h, err := term.GetSize(int(os.Stdout.Fd())); err == nil {
				cols, rows = w, h
			}
			e, err := cl.Exec(ctx, args[0], client.ExecRequest{
				Argv: []string{"/bin/bash", "-l"}, TTY: true, KeepStdinOpen: true, User: user, Wait: client.Bool(false),
				Env: map[string]string{"COLUMNS": fmt.Sprint(cols), "LINES": fmt.Sprint(rows)}, TimeoutS: 7200, Sync: client.Bool(false),
			})
			if err != nil {
				return err
			}
			if term.IsTerminal(int(os.Stdin.Fd())) {
				old, err := term.MakeRaw(int(os.Stdin.Fd()))
				if err == nil {
					defer term.Restore(int(os.Stdin.Fd()), old)
				}
			}
			// stdin -> sandbox, batched every 15ms.
			var mu sync.Mutex
			var pending []byte
			go func() {
				buf := make([]byte, 4096)
				for {
					n, err := os.Stdin.Read(buf)
					if n > 0 {
						mu.Lock()
						pending = append(pending, buf[:n]...)
						mu.Unlock()
					}
					if err != nil {
						return
					}
				}
			}()
			go func() {
				t := time.NewTicker(15 * time.Millisecond)
				defer t.Stop()
				for {
					select {
					case <-ctx.Done():
						return
					case <-t.C:
						mu.Lock()
						data := pending
						pending = nil
						mu.Unlock()
						if len(data) > 0 {
							_ = stdinWrite(ctx, cl, e.ID, data)
						}
					}
				}
			}()
			return cl.Stream(ctx, e.ID, 0, func(f client.StreamFrame) error {
				if f.Stream == "stdout" || f.Stream == "stderr" {
					_, _ = os.Stdout.Write([]byte(f.Data))
				}
				return nil
			})
		},
	}
	c.Flags().StringVar(&user, "user", "", "user (default agent; root for root)")
	return c
}

func stdinWrite(ctx context.Context, cl *client.Client, execID string, data []byte) error {
	return cl.WriteStdin(ctx, execID, data)
}
