package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

const managedSkillMarker = ".managed-by-suppyhq-cli"

type skillRefreshState struct {
	CLIVersion  string    `json:"cli_version"`
	RefreshedAt time.Time `json:"refreshed_at"`
}

func skillRefreshPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".suppyhq", "skill_refresh.json")
}

func readSkillRefreshState() *skillRefreshState {
	data, err := os.ReadFile(skillRefreshPath())
	if err != nil {
		return nil
	}
	var state skillRefreshState
	if json.Unmarshal(data, &state) != nil {
		return nil
	}
	return &state
}

func writeSkillRefreshState(state *skillRefreshState) {
	if err := os.MkdirAll(filepath.Dir(skillRefreshPath()), 0o700); err != nil {
		return
	}
	data, err := json.Marshal(state)
	if err != nil {
		return
	}
	_ = os.WriteFile(skillRefreshPath(), data, 0o600)
}

func isManagedSkillDir(skillDir string) bool {
	_, err := os.Stat(filepath.Join(skillDir, managedSkillMarker))
	return err == nil
}

func writeManagedSkill(abs string, stdout io.Writer, agentName, version string) error {
	if version == "" {
		version = Version
	}
	skillDir := filepath.Dir(abs)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(abs, []byte(skillMarkdown), 0o644); err != nil {
		return err
	}
	marker := fmt.Sprintf("suppyhq-cli %s\n", version)
	if err := os.WriteFile(filepath.Join(skillDir, managedSkillMarker), []byte(marker), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "[%s] installed: %s\n", agentName, abs)
	return nil
}

// maybeRefreshManagedSkills updates CLI-owned skill copies once per CLI version.
func maybeRefreshManagedSkills() {
	if Version == "dev" {
		return
	}
	state := readSkillRefreshState()
	if state != nil && state.CLIVersion == Version {
		return
	}
	refreshManagedSkillsAt(Version)
	writeSkillRefreshState(&skillRefreshState{CLIVersion: Version, RefreshedAt: time.Now()})
}

func refreshManagedSkills() int {
	return refreshManagedSkillsAt(Version)
}

func refreshManagedSkillsAt(version string) int {
	refreshed := 0
	for _, t := range skillTargets {
		if t.scope == "project" {
			continue
		}
		abs, err := t.absPath()
		if err != nil {
			continue
		}
		skillDir := filepath.Dir(abs)
		if !isManagedSkillDir(skillDir) {
			continue
		}
		if err := writeManagedSkill(abs, io.Discard, t.name, version); err != nil {
			continue
		}
		refreshed++
	}
	return refreshed
}
