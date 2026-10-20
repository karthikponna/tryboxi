// Package agentrules embeds the rule/skill text installed for harnesses.
package agentrules

import _ "embed"

//go:embed sandbox.md
var Sandbox string

// Cursor wraps the rule as an always-applied Cursor rule.
func Cursor() string {
	return "---\ndescription: Run tests, installs, and untrusted code in the local sandboxd microVM\nalwaysApply: true\n---\n\n" + Sandbox
}

// ClaudeSkill wraps the rule as a Claude Code skill.
func ClaudeSkill() string {
	return "---\nname: sandbox\ndescription: Run tests, builds, installs, servers, and untrusted code in a disposable local Linux microVM via the sandbox MCP tools.\n---\n\n" + Sandbox
}
