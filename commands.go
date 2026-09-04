package main

import (
	"fmt"
	"io"
)

type commandSpec struct {
	Name        string         `json:"name"`
	Path        string         `json:"path"`
	Short       string         `json:"short"`
	Subcommands []commandSpec  `json:"subcommands,omitempty"`
	Flags       []flagSpec     `json:"flags,omitempty"`
	Notes       []string       `json:"notes,omitempty"`
}

type flagSpec struct {
	Name    string `json:"name"`
	Shorthand string `json:"shorthand,omitempty"`
	Type    string `json:"type"`
	Default string `json:"default,omitempty"`
	Usage   string `json:"usage"`
}

func commandCatalog() []commandSpec {
	return []commandSpec{
		{
			Name:  "auth",
			Path:  "suppyhq auth",
			Short: "Authenticate with SuppyHQ",
			Subcommands: []commandSpec{
				{Name: "login", Path: "suppyhq auth login", Short: "Browser OAuth (default) or --manual paste flow", Flags: []flagSpec{
					{Name: "manual", Type: "bool", Usage: "Paste Client ID + Secret instead of browser OAuth"},
					{Name: "name", Type: "string", Usage: "Agent name for browser OAuth"},
				}, Notes: []string{"Browser login is default. Token never touches clipboard."}},
				{Name: "status", Path: "suppyhq auth status", Short: "Show who is authenticated"},
				{Name: "logout", Path: "suppyhq auth logout", Short: "Forget stored credentials"},
			},
		},
		{Name: "inbox", Path: "suppyhq inbox", Short: "List conversations", Notes: []string{"Requires read scope"}},
		{Name: "thread", Path: "suppyhq thread <id>", Short: "Show one conversation with messages", Notes: []string{"Requires read scope"}},
		{Name: "customers", Path: "suppyhq customers", Short: "List customers", Notes: []string{"VIP customers have vip_at set"}},
		{
			Name:  "reply",
			Path:  "suppyhq reply <id> [body]",
			Short: "Post a reply or save a draft",
			Flags: []flagSpec{{Name: "draft", Shorthand: "d", Type: "bool", Usage: "Save as draft for operator review"}},
			Notes: []string{"Body via 2nd arg, stdin, or echo pipe", "Default send queues 30s cancel window", "Writes are never auto-retried on 429"},
		},
		{Name: "setup", Path: "suppyhq setup", Short: "Install skills and agent plugins", Subcommands: []commandSpec{
			{Name: "claude", Path: "suppyhq setup claude", Short: "Claude Code plugin + skill + MCP hint"},
			{Name: "cursor", Path: "suppyhq setup cursor", Short: "Cursor skill (project-scoped)"},
			{Name: "codex", Path: "suppyhq setup codex", Short: "Codex skill"},
			{Name: "agents", Path: "suppyhq setup agents", Short: "Skill + every detected agent"},
		}},
		{Name: "doctor", Path: "suppyhq doctor", Short: "Check CLI, auth, skill, and plugin health"},
		{Name: "mcp", Path: "suppyhq mcp", Short: "MCP server on stdin/stdout", Flags: []flagSpec{
			{Name: "read-only", Type: "bool", Usage: "Serve read-only actions only"},
			{Name: "domains", Type: "string", Usage: "Comma-separated domains: conversations,customers,identity"},
		}, Notes: []string{"Register: claude mcp add suppyhq -- suppyhq mcp"}},
		{Name: "commands", Path: "suppyhq commands", Short: "List the command catalog (--json)"},
		{Name: "install-skill", Path: "suppyhq install-skill", Short: "Install embedded SKILL.md for an agent"},
		{Name: "upgrade", Path: "suppyhq upgrade", Short: "Self-update from GitHub releases"},
	}
}

func runCommands(args []string, stdout io.Writer) int {
	ff, _ := parseFormatFlags(args)
	catalog := commandCatalog()
	if ff.JSON || ff.Agent || !isTTY(stdout) {
		return writeJSON(stdout, map[string]any{"commands": catalog})
	}
	for _, c := range catalog {
		fmt.Fprintf(stdout, "%s — %s\n", c.Path, c.Short)
	}
	return exitOK
}

func agentHelpFor(path string) (commandSpec, bool) {
	for _, c := range commandCatalog() {
		if c.Path == path || c.Name == path {
			return c, true
		}
		for _, sub := range c.Subcommands {
			if sub.Path == path || sub.Name == path {
				return sub, true
			}
		}
	}
	return commandSpec{}, false
}

func runAgentHelp(topic string, stdout io.Writer) int {
	spec, ok := agentHelpFor(topic)
	if !ok {
		spec = commandSpec{
			Name:  "suppyhq",
			Path:  "suppyhq",
			Short: "Official CLI for SuppyHQ",
			Subcommands: commandCatalog(),
			Flags: []flagSpec{
				{Name: "json", Shorthand: "j", Type: "bool", Usage: "JSON envelope"},
				{Name: "quiet", Shorthand: "q", Type: "bool", Usage: "Raw JSON data only"},
				{Name: "agent", Type: "bool", Usage: "Agent mode: raw data, no prompts"},
			},
			Notes: []string{"Use suppyhq commands --json for the full catalog"},
		}
	}
	return writeJSON(stdout, spec)
}

func printHelp(stdout io.Writer, args []string) int {
	for _, a := range args {
		if a == "--agent" {
			topic := "suppyhq"
			if len(args) > 1 {
				for _, x := range args {
					if x != "--agent" && !stringsHasPrefix(x, "-") {
						topic = "suppyhq " + x
						break
					}
				}
			}
			return runAgentHelp(topic, stdout)
		}
	}
	usage(stdout)
	return exitOK
}

func stringsHasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}
