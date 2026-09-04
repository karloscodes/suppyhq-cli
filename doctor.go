package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type doctorCheck struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Message string `json:"message"`
	Hint    string `json:"hint,omitempty"`
}

func runDoctor(args []string, stdout, stderr io.Writer) int {
	ff, _ := parseFormatFlags(args)
	mode := ff.mode()

	checks := []doctorCheck{
		checkCLI(),
		checkAuth(),
		checkClaudeSkill(),
		checkClaudePlugin(),
	}

	crumbs := doctorBreadcrumbs(checks)
	data := map[string]any{"checks": checks}

	if machineOutput(mode, stdout) {
		env := successEnvelope{OK: true, Data: data, Summary: doctorSummary(checks), Breadcrumbs: crumbs}
		return writeJSON(stdout, env)
	}

	for _, c := range checks {
		icon := "✓"
		if c.Status == "warn" {
			icon = "!"
		}
		if c.Status == "fail" {
			icon = "✗"
		}
		fmt.Fprintf(stdout, "%s %s: %s\n", icon, c.Name, c.Message)
		if c.Hint != "" && c.Status != "pass" {
			fmt.Fprintf(stdout, "  → %s\n", c.Hint)
		}
	}
	return exitOK
}

func doctorSummary(checks []doctorCheck) string {
	fails := 0
	for _, c := range checks {
		if c.Status == "fail" {
			fails++
		}
	}
	if fails == 0 {
		return "All checks passed"
	}
	return fmt.Sprintf("%d check(s) need attention", fails)
}

func doctorBreadcrumbs(checks []doctorCheck) []breadcrumb {
	for _, c := range checks {
		if c.Status == "fail" && c.Hint != "" {
			return []breadcrumb{{Action: "fix", Cmd: strings.TrimPrefix(c.Hint, "Run: ")}}
		}
	}
	return []breadcrumb{{Action: "triage", Cmd: "suppyhq inbox"}}
}

func checkCLI() doctorCheck {
	return doctorCheck{Name: "CLI", Status: "pass", Message: fmt.Sprintf("suppyhq %s", Version)}
}

func checkAuth() doctorCheck {
	cfg, err := loadConfig()
	if err != nil {
		return doctorCheck{Name: "Auth", Status: "fail", Message: "Config unreadable", Hint: "Run: suppyhq auth login"}
	}
	if cfg.AccessToken == "" && (cfg.ClientID == "" || cfg.ClientSecret == "") {
		return doctorCheck{Name: "Auth", Status: "fail", Message: "Not authenticated", Hint: "Run: suppyhq auth login"}
	}
	if _, err := fetchToken(cfg); err != nil {
		return doctorCheck{Name: "Auth", Status: "fail", Message: "Token rejected", Hint: "Run: suppyhq auth login"}
	}
	name := cfg.AgentName
	if name == "" {
		name = cfg.ClientID
	}
	return doctorCheck{Name: "Auth", Status: "pass", Message: fmt.Sprintf("Authenticated as %s", name)}
}

func checkClaudeSkill() doctorCheck {
	home, err := os.UserHomeDir()
	if err != nil {
		return doctorCheck{Name: "Claude Skill", Status: "skip", Message: "Cannot determine home directory"}
	}
	skillDir := filepath.Join(home, ".claude", "skills", "suppyhq")
	path := filepath.Join(skillDir, "SKILL.md")
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return doctorCheck{Name: "Claude Skill", Status: "warn", Message: "Not installed", Hint: "Run: suppyhq setup claude"}
		}
		return doctorCheck{Name: "Claude Skill", Status: "warn", Message: "Cannot check skill"}
	}
	if isManagedSkillDir(skillDir) {
		return doctorCheck{Name: "Claude Skill", Status: "pass", Message: "Installed (managed by suppyhq-cli)"}
	}
	return doctorCheck{Name: "Claude Skill", Status: "warn", Message: "Installed but not managed", Hint: "Run: suppyhq install-skill --force to adopt CLI-managed updates"}
}

func checkClaudePlugin() doctorCheck {
	if !detectClaude() {
		return doctorCheck{Name: "Claude Plugin", Status: "skip", Message: "Claude Code not detected"}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return doctorCheck{Name: "Claude Plugin", Status: "skip", Message: "Cannot determine home directory"}
	}
	data, err := os.ReadFile(filepath.Join(home, ".claude", "plugins", "installed_plugins.json"))
	if err != nil {
		return doctorCheck{Name: "Claude Plugin", Status: "warn", Message: "Plugin not installed", Hint: "Run: suppyhq setup claude"}
	}
	if pluginInstalledJSON(data) {
		return doctorCheck{Name: "Claude Plugin", Status: "pass", Message: "Installed"}
	}
	return doctorCheck{Name: "Claude Plugin", Status: "warn", Message: "Plugin not installed", Hint: "Run: suppyhq setup claude"}
}

func pluginInstalledJSON(data []byte) bool {
	s := string(data)
	return strings.Contains(s, `"suppyhq@suppyhq"`) || strings.Contains(s, `"suppyhq"`)
}

func detectClaude() bool {
	if _, err := exec.LookPath("claude"); err == nil {
		return true
	}
	home, _ := os.UserHomeDir()
	info, err := os.Stat(filepath.Join(home, ".claude"))
	return err == nil && info.IsDir()
}

func findClaudeBinary() string {
	if p, err := exec.LookPath("claude"); err == nil {
		return p
	}
	home, _ := os.UserHomeDir()
	candidate := filepath.Join(home, ".local", "bin", "claude")
	if _, err := os.Stat(candidate); err == nil {
		return candidate
	}
	return ""
}

func detectedAgents() []string {
	var agents []string
	if detectClaude() {
		agents = append(agents, "claude")
	}
	home, _ := os.UserHomeDir()
	if info, err := os.Stat(filepath.Join(home, ".codex")); err == nil && info.IsDir() {
		agents = append(agents, "codex")
	}
	if info, err := os.Stat(filepath.Join(home, ".config", "opencode")); err == nil && info.IsDir() {
		agents = append(agents, "opencode")
	}
	if _, err := os.Stat(filepath.Join(".cursor")); err == nil {
		agents = append(agents, "cursor")
	}
	return agents
}

// runAgentHookSessionStart prints session context for plugin hooks.
func runAgentHookSessionStart(stdout io.Writer) int {
	check := checkAuth()
	payload := map[string]any{
		"cli_version":   Version,
		"authenticated": check.Status == "pass",
		"message":       check.Message,
	}
	if check.Hint != "" {
		payload["hint"] = check.Hint
	}
	if vc := readVersionCheck(); vc != nil && vc.Latest != "" && isNewerVersion(vc.Latest, Version) {
		payload["upgrade_available"] = vc.Latest
		payload["upgrade_hint"] = "Run: suppyhq upgrade"
	}
	out, _ := json.Marshal(payload)
	fmt.Fprintln(stdout, string(out))
	return exitOK
}
