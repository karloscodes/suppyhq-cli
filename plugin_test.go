package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// Other tests in this package chdir into temp directories, so these read
// from the repo root resolved off this source file rather than from the
// working directory, which is not ours to rely on.
func repoFile(t *testing.T, rel string) []byte {
	t.Helper()

	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot resolve the path of plugin_test.go")
	}

	raw, err := os.ReadFile(filepath.Join(filepath.Dir(thisFile), rel))
	if err != nil {
		t.Fatalf("reading %s: %v", rel, err)
	}
	return raw
}

// The skill ships twice: once at skills/ for the Agent Plugins standard
// (which Cursor and Codex read) and once inside .claude-plugin/ because
// Claude Code treats that directory as the plugin root. Nothing in the
// build copies one to the other, so without this test they drift and the
// Claude Code plugin quietly ships a stale skill.
func TestSkillCopiesAreIdentical(t *testing.T) {
	canonical := repoFile(t, "skills/suppyhq/SKILL.md")
	copied := repoFile(t, ".claude-plugin/skills/suppyhq/SKILL.md")

	if string(canonical) != string(copied) {
		t.Error("skills/suppyhq/SKILL.md and .claude-plugin/skills/suppyhq/SKILL.md have drifted.\n" +
			"Copy the canonical one over: cp skills/suppyhq/SKILL.md .claude-plugin/skills/suppyhq/SKILL.md")
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
