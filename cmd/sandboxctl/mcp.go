package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	agentrules "github.com/karthikponna/tryboxi/agent-rules"
)

func mcpBinary() (string, error) {
	if exe, err := os.Executable(); err == nil {
		exe, _ = filepath.EvalSymlinks(exe)
		p := filepath.Join(filepath.Dir(exe), "sandbox-mcp")
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	if p, err := exec.LookPath("sandbox-mcp"); err == nil {
		return filepath.Abs(p)
	}
	return "", errors.New("sandbox-mcp not found next to sandboxctl or on PATH; run `make build`")
}

func writeFileMkdir(p, content string) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(content), 0o644)
}

// mergeMCPJSON sets mcpServers.sandbox in a Cursor-style JSON config.
func mergeMCPJSON(path string, server map[string]any) error {
	cfg := map[string]any{}
	if b, err := os.ReadFile(path); err == nil && len(strings.TrimSpace(string(b))) > 0 {
		if err := json.Unmarshal(b, &cfg); err != nil {
			return fmt.Errorf("%s is not valid JSON (%v); not modifying it", path, err)
		}
		if err := os.WriteFile(path+".bak", b, 0o644); err != nil {
			return err
		}
	}
	servers, _ := cfg["mcpServers"].(map[string]any)
	if servers == nil {
		servers = map[string]any{}
	}
	servers["sandbox"] = server
	cfg["mcpServers"] = servers
	b, _ := json.MarshalIndent(cfg, "", "  ")
	return writeFileMkdir(path, string(b)+"\n")
}

func mcpCmd() *cobra.Command {
	mcp := &cobra.Command{Use: "mcp", Short: "Connect harnesses (Cursor, Claude Code, Codex) to sandboxd"}
	var project string
	var global bool
	install := &cobra.Command{
		Use:   "install cursor|claude|codex",
		Short: "Write the MCP server config and agent rules for a harness",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			bin, err := mcpBinary()
			if err != nil {
				return err
			}
			if project == "" {
				project, _ = os.Getwd()
			}
			project, _ = filepath.Abs(project)
			switch args[0] {
			case "cursor":
				cfgPath := filepath.Join(project, ".cursor", "mcp.json")
				if global {
					home, _ := os.UserHomeDir()
					cfgPath = filepath.Join(home, ".cursor", "mcp.json")
				}
				server := map[string]any{"command": bin, "args": []string{}, "env": map[string]string{"SANDBOX_WORKSPACE": "${workspaceFolder}", "SANDBOX_CLIENT": "cursor"}}
				if err := mergeMCPJSON(cfgPath, server); err != nil {
					return err
				}
				fmt.Println("wrote", cfgPath)
				rule := filepath.Join(project, ".cursor", "rules", "sandbox.mdc")
				if err := writeFileMkdir(rule, agentrules.Cursor()); err != nil {
					return err
				}
				fmt.Println("wrote", rule)
				fmt.Println("Next: open Cursor Settings -> MCP and enable the 'sandbox' server (reload the window if it is not listed).")
			case "claude":
				skill := filepath.Join(project, ".claude", "skills", "sandbox", "SKILL.md")
				if err := writeFileMkdir(skill, agentrules.ClaudeSkill()); err != nil {
					return err
				}
				fmt.Println("wrote", skill)
				scope := "project"
				if global {
					scope = "user"
				}
				claudeArgs := []string{"mcp", "add", "--scope", scope, "--env", "SANDBOX_CLIENT=claude", "sandbox", "--", bin}
				if _, err := exec.LookPath("claude"); err == nil {
					c := exec.Command("claude", claudeArgs...)
					c.Dir = project
					c.Stdout, c.Stderr = os.Stdout, os.Stderr
					if err := c.Run(); err != nil {
						return err
					}
				} else {
					fmt.Println("claude CLI not found; run this yourself:\n  claude " + strings.Join(claudeArgs, " "))
				}
			case "codex":
				home, _ := os.UserHomeDir()
				cfgPath := filepath.Join(home, ".codex", "config.toml")
				b, _ := os.ReadFile(cfgPath)
				if strings.Contains(string(b), "[mcp_servers.sandbox]") {
					fmt.Println(cfgPath, "already has [mcp_servers.sandbox]; leaving it unchanged")
				} else {
					if len(b) > 0 {
						_ = os.WriteFile(cfgPath+".bak", b, 0o600)
					}
					block := fmt.Sprintf("\n[mcp_servers.sandbox]\ncommand = %q\nargs = []\nenv = { SANDBOX_CLIENT = \"codex\" }\nstartup_timeout_sec = 30\ntool_timeout_sec = 900\n", bin)
					if err := writeFileMkdir(cfgPath, string(b)+block); err != nil {
						return err
					}
					fmt.Println("updated", cfgPath)
				}
				agents := filepath.Join(project, "AGENTS.md")
				if ab, err := os.ReadFile(agents); err == nil {
					if !strings.Contains(string(ab), "sandbox_exec") {
						_ = os.WriteFile(agents+".bak", ab, 0o644)
						if err := os.WriteFile(agents, append(ab, []byte("\n"+agentrules.Sandbox)...), 0o644); err != nil {
							return err
						}
						fmt.Println("appended sandbox rules to", agents, "(backup in AGENTS.md.bak)")
					}
				} else {
					if err := writeFileMkdir(agents, agentrules.Sandbox); err != nil {
						return err
					}
					fmt.Println("wrote", agents)
				}
			default:
				return fmt.Errorf("unknown harness %q (cursor, claude, codex)", args[0])
			}
			return nil
		},
	}
	install.Flags().StringVar(&project, "project", "", "project directory (default: current directory)")
	install.Flags().BoolVar(&global, "global", false, "install for the user instead of the project (Cursor: ~/.cursor/mcp.json; Claude: --scope user)")
	mcp.AddCommand(install)
	return mcp
}
