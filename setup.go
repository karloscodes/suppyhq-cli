package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
)

const (
	claudeMarketplaceRepo = "https://github.com/karloscodes/suppyhq-cli"
	claudePluginKey       = "suppyhq@suppyhq"
	agentSetupEnv         = "SUPPYHQ_SETUP_AGENT"
)

func runSetup(args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		fmt.Fprintln(stderr, "suppyhq: setup: usage — suppyhq setup (claude | cursor | codex | agents)")
		return exitUsage
	}
	switch args[0] {
	case "claude":
		return setupClaude(stdout, stderr)
	case "cursor":
		return setupCursor(stdout, stderr)
	case "codex":
		return setupOneAgent("codex", stdout, stderr)
	case "opencode":
		return setupOneAgent("opencode", stdout, stderr)
	case "agents":
		return setupAgents(stdout, stderr)
	default:
		fmt.Fprintf(stderr, "suppyhq: unknown setup target: %s\n", args[0])
		return exitUsage
	}
}

// setupAgents installs the baseline skill and connects coding agents without
// prompting. Selection is controlled by SUPPYHQ_SETUP_AGENT (claude, codex,
// cursor, opencode, all, none). When unset, a single detected agent is
// connected; when several are detected, only the skill is installed.
func setupAgents(stdout, stderr io.Writer) int {
	if err := runInstallSkill([]string{}, stdout); err != nil {
		fmt.Fprintf(stderr, "suppyhq: skill: %v\n", err)
		return exitAPI
	}

	selectorRaw := strings.TrimSpace(os.Getenv(agentSetupEnv))
	selector := strings.ToLower(selectorRaw)
	detected := detectedAgents()

	var targets []string
	switch selector {
	case "", "auto":
		switch len(detected) {
		case 0:
			fmt.Fprintln(stdout, "Skill installed. No coding agents detected.")
			printAgentNextSteps(stdout)
			return exitOK
		case 1:
			targets = detected
		default:
			fmt.Fprintln(stdout, "Skill installed. Multiple coding agents detected — pick one:")
			for _, id := range detected {
				fmt.Fprintf(stdout, "  suppyhq setup %s\n", id)
			}
			return exitOK
		}
	case "all":
		targets = detected
	case "none":
		fmt.Fprintln(stdout, "Skill installed.")
		return exitOK
	case "claude", "codex", "cursor", "opencode":
		targets = []string{selector}
	default:
		fmt.Fprintf(stdout, "Unknown %s %q — installed skill only (expected claude, codex, cursor, opencode, all, or none).\n", agentSetupEnv, selectorRaw)
		return exitOK
	}

	code := exitOK
	for _, id := range sortStringsCopy(targets) {
		switch id {
		case "claude":
			if c := setupClaude(stdout, stderr); c != exitOK {
				code = c
			}
		case "cursor":
			if c := setupCursor(stdout, stderr); c != exitOK {
				code = c
			}
		default:
			if c := setupOneAgent(id, stdout, stderr); c != exitOK {
				code = c
			}
		}
	}
	return code
}

func printAgentNextSteps(stdout io.Writer) {
	fmt.Fprintln(stdout, "Run: suppyhq setup claude   (after installing Claude Code)")
}

func sortStringsCopy(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

func setupClaude(stdout, stderr io.Writer) int {
	if err := runInstallSkill([]string{"--target=claude"}, stdout); err != nil {
		fmt.Fprintf(stderr, "suppyhq: skill: %v\n", err)
		return exitAPI
	}

	claudePath := findClaudeBinary()
	if claudePath == "" {
		fmt.Fprintln(stdout, "Claude Code skill installed.")
		fmt.Fprintln(stdout, "Install Claude Code, then rerun: suppyhq setup claude")
		printMCPHint(stdout)
		return exitOK
	}

	mkt := exec.Command(claudePath, "plugin", "marketplace", "add", claudeMarketplaceRepo)
	mkt.Stdout = stdout
	mkt.Stderr = stderr
	if err := mkt.Run(); err != nil {
		fmt.Fprintf(stdout, "Note: marketplace add returned %v (may already be registered)\n", err)
	}

	install := exec.Command(claudePath, "plugin", "install", claudePluginKey)
	install.Stdout = stdout
	install.Stderr = stderr
	if err := install.Run(); err != nil {
		fmt.Fprintf(stdout, "Plugin install failed: %v\n", err)
		fmt.Fprintln(stdout, "Install manually:")
		fmt.Fprintf(stdout, "  claude plugin marketplace add %s\n", claudeMarketplaceRepo)
		fmt.Fprintf(stdout, "  claude plugin install %s\n", claudePluginKey)
	} else {
		fmt.Fprintln(stdout, "Claude Code plugin installed.")
		fmt.Fprintln(stdout, "In Claude Code: /plugins → enable auto-update for suppyhq@suppyhq.")
	}

	printMCPHint(stdout)
	fmt.Fprintln(stdout, "Restart Claude Code to load the plugin and skill.")
	return exitOK
}

func setupCursor(stdout, stderr io.Writer) int {
	if err := runInstallSkill([]string{"--target=cursor"}, stdout); err != nil {
		fmt.Fprintf(stderr, "suppyhq: %v\n", err)
		return exitAPI
	}
	printMCPHint(stdout)
	fmt.Fprintln(stdout, "Restart Cursor to pick up the skill.")
	return exitOK
}

func setupOneAgent(target string, stdout, stderr io.Writer) int {
	if err := runInstallSkill([]string{"--target=" + target}, stdout); err != nil {
		fmt.Fprintf(stderr, "suppyhq: %v\n", err)
		return exitAPI
	}
	fmt.Fprintf(stdout, "Restart your %s session to pick up the skill.\n", target)
	return exitOK
}

func printMCPHint(stdout io.Writer) {
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "Optional — register MCP for tool-based access:")
	fmt.Fprintln(stdout, "  claude mcp add suppyhq -- suppyhq mcp")
	fmt.Fprintln(stdout, "  claude mcp add suppyhq-readonly -- suppyhq mcp --read-only")
}

func runAgentHook(args []string, stdout io.Writer) int {
	if len(args) < 1 {
		return exitOK
	}
	switch args[0] {
	case "session-start":
		return runAgentHookSessionStart(stdout)
	default:
		return exitOK
	}
}
