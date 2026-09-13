package main

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

func repoRoot(t *testing.T) string {
	t.Helper()

	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot resolve the path of plugin_test.go")
	}
	return filepath.Dir(thisFile)
}

// Other tests in this package chdir into temp directories, so these read
// from the repo root resolved off this source file rather than from the
// working directory, which is not ours to rely on.
func repoFile(t *testing.T, rel string) []byte {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join(repoRoot(t), rel))
	if err != nil {
		t.Fatalf("reading %s: %v", rel, err)
	}
	return raw
}

// One skill file, and only one. Every plugin manifest points at skills/,
// the binary embeds it (see the go:embed in main.go), and the moment a
// second copy appears somewhere it starts going stale in silence.
func TestExactlyOneSkillFile(t *testing.T) {
	root := repoRoot(t)

	var found []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == ".git" || d.Name() == "node_modules") {
			return fs.SkipDir
		}
		if d.Name() == "SKILL.md" {
			rel, _ := filepath.Rel(root, path)
			found = append(found, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the repo: %v", err)
	}

	if len(found) != 1 || found[0] != filepath.Join("skills", "suppyhq", "SKILL.md") {
		t.Errorf("expected exactly skills/suppyhq/SKILL.md, found %v", found)
	}
}

// Four manifests name the same plugin, and a version that disagrees across
// them means one client installs something it shouldn't.
func TestPluginManifestsAgree(t *testing.T) {
	manifests := map[string]string{
		"plugin.json":                     "", // Agent Plugins 1.0.0 (Cursor, Codex)
		".claude-plugin/plugin.json":      "", // Claude Code
		".cursor-plugin/plugin.json":      "", // Cursor native
		"claude-plugins/marketplace.json": "",
	}

	versions := map[string]string{}
	for path := range manifests {
		raw := repoFile(t, path)

		var parsed map[string]any
		if err := json.Unmarshal(raw, &parsed); err != nil {
			t.Fatalf("%s is not valid JSON: %v", path, err)
		}

		// The marketplace file wraps its entry in a plugins array.
		if plugins, ok := parsed["plugins"].([]any); ok {
			if len(plugins) != 1 {
				t.Fatalf("%s: expected exactly one plugin entry, got %d", path, len(plugins))
			}
			parsed = plugins[0].(map[string]any)
		}

		if name, _ := parsed["name"].(string); name != "suppyhq" {
			t.Errorf("%s: name is %q, want \"suppyhq\"", path, name)
		}
		if v, ok := parsed["version"].(string); ok {
			versions[path] = v
		}
	}

	var first, firstPath string
	for path, v := range versions {
		if first == "" {
			first, firstPath = v, path
			continue
		}
		if v != first {
			t.Errorf("version mismatch: %s says %q, %s says %q", firstPath, first, path, v)
		}
	}
}

// The MCP server every client registers is the CLI's own `suppyhq mcp`.
func TestMCPManifestPointsAtThisCLI(t *testing.T) {
	raw := repoFile(t, "mcp.json")

	var parsed struct {
		Schema     string `json:"$schema"`
		MCPServers map[string]struct {
			Type    string   `json:"type"`
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("mcp.json is not valid JSON: %v", err)
	}

	if parsed.Schema != "https://agent-plugins.org/schemas/1.0.0/mcp.schema.json" {
		t.Errorf("mcp.json $schema is %q", parsed.Schema)
	}

	server, ok := parsed.MCPServers["suppyhq"]
	if !ok {
		t.Fatal("mcp.json declares no \"suppyhq\" server")
	}
	if server.Type != "stdio" {
		t.Errorf("server type is %q, want \"stdio\"", server.Type)
	}
	if server.Command != "suppyhq" {
		t.Errorf("server command is %q, want \"suppyhq\"", server.Command)
	}
	if len(server.Args) != 1 || server.Args[0] != "mcp" {
		t.Errorf("server args are %v, want [mcp]", server.Args)
	}
}

// Every `suppyhq ...` command the skill tells an agent to run has to exist.
//
// This is the drift that actually costs something: the skill is the contract
// an agent reads, so a renamed command or a dropped flag means the agent
// confidently runs something that fails. basecamp-cli checks this against a
// generated .surface snapshot; commandCatalog() already is that surface, so
// there's nothing to generate.
func TestSkillOnlyReferencesRealCommands(t *testing.T) {
	paths := map[string]bool{}
	flags := map[string]bool{}

	// Catalog paths carry argument placeholders ("suppyhq thread <id>",
	// "suppyhq reply <id> [body]"). Index the command words only.
	commandWords := func(path string) string {
		var words []string
		for _, w := range strings.Fields(path) {
			if strings.HasPrefix(w, "<") || strings.HasPrefix(w, "[") {
				break
			}
			words = append(words, w)
		}
		return strings.Join(words, " ")
	}

	var walk func(specs []commandSpec)
	walk = func(specs []commandSpec) {
		for _, spec := range specs {
			paths[commandWords(spec.Path)] = true
			for _, f := range spec.Flags {
				flags["--"+f.Name] = true
			}
			walk(spec.Subcommands)
		}
	}
	walk(commandCatalog())

	allowed := map[string]bool{}
	for _, line := range strings.Split(string(repoFile(t, ".skill-drift-allowlist")), "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			allowed[line] = true
		}
	}

	skill := string(repoFile(t, "skills/suppyhq/SKILL.md"))

	// Longest match wins: "suppyhq setup claude" before "suppyhq setup".
	commandRef := regexp.MustCompile(`suppyhq(?: [a-z][a-z-]*){1,3}`)
	for _, ref := range commandRef.FindAllString(skill, -1) {
		words := strings.Fields(ref)
		resolved := false
		for i := len(words); i > 1; i-- {
			if paths[strings.Join(words[:i], " ")] {
				resolved = true
				break
			}
		}
		if !resolved && !allowed[ref] {
			t.Errorf("SKILL.md references %q, which is not in commandCatalog(). "+
				"Add it to the catalog, or to .skill-drift-allowlist if the skill "+
				"mentions it on purpose.", ref)
		}
	}

	// Global flags from usage() apply to every command, so no single
	// catalog entry declares them.
	for _, global := range []string{"--help", "--version", "--json", "--quiet", "--agent"} {
		flags[global] = true
	}
	flagRef := regexp.MustCompile(`--[a-z][a-z-]*`)
	for _, f := range flagRef.FindAllString(skill, -1) {
		if !flags[f] && !allowed[f] {
			t.Errorf("SKILL.md references flag %q, which no command in commandCatalog() declares", f)
		}
	}
}
