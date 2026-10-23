package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/karthikponna/tryboxi/pkg/client"
)

func netCmd() *cobra.Command {
	var allow, deny, remove []string
	var mode, reason string
	c := &cobra.Command{
		Use:   "net SANDBOX",
		Short: "Show or change a sandbox's network policy (live, no restart)",
		Example: `  sandboxctl net sbx_123                          # policy + recently blocked hosts
  sandboxctl net sbx_123 --allow api.stripe.com   # open one host
  sandboxctl net sbx_123 --allow @browsers        # open a preset
  sandboxctl net sbx_123 --mode none              # cut the network`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cx, cancel := ctx()
			defer cancel()
			cl := cli()
			var info client.NetworkInfo
			var err error
			if len(allow)+len(deny)+len(remove) > 0 || mode != "" {
				info, err = cl.UpdateNetwork(cx, args[0], client.NetworkUpdate{Mode: mode, Allow: allow, Deny: deny, Remove: remove, Reason: reason})
			} else {
				info, err = cl.Network(cx, args[0])
			}
			if err != nil {
				return err
			}
			if flagJSON {
				printJSON(info)
				return nil
			}
			fmt.Printf("mode: %s\nallow: %s\n", info.Mode, strings.Join(info.Allow, " "))
			if len(info.Deny) > 0 {
				fmt.Printf("deny: %s\n", strings.Join(info.Deny, " "))
			}
			if len(info.RecentDenied) > 0 {
				fmt.Println("recently blocked:")
				for _, d := range info.RecentDenied {
					fmt.Printf("  %s  %-35s %s\n", time.UnixMilli(d.At).Format("15:04:05"), d.Host, d.Reason)
				}
			}
			return nil
		},
	}
	c.Flags().StringSliceVar(&allow, "allow", nil, "hosts, *.wildcards, CIDRs, or @presets to allow")
	c.Flags().StringSliceVar(&deny, "deny", nil, "hosts to deny")
	c.Flags().StringSliceVar(&remove, "remove", nil, "remove entries from the allowlist")
	c.Flags().StringVar(&mode, "mode", "", "none | allowlist | full")
	c.Flags().StringVar(&reason, "reason", "", "why (recorded in events)")
	return c
}
