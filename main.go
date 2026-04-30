// suppyhq-cli is the official CLI for SuppyHQ. Talks to the Agents API
// using OAuth2 client-credentials.
//
// Usage:
//
//	suppyhq auth login                  # interactive — paste credentials
//	suppyhq auth status                 # show who's authenticated
//	suppyhq auth logout                 # forget credentials
//	suppyhq install-skill               # install Claude Code skill
//	suppyhq inbox                       # list conversations
//	suppyhq thread <conversation_id>    # show a thread with messages
//	suppyhq customers                   # list customers
//	suppyhq reply <conversation_id> <html_body>     # body inline
//	echo "<p>Hi</p>" | suppyhq reply <conversation_id>   # body via stdin
//
// Configuration lives at ~/.suppyhq/config.json. Env vars override:
//
//	SUPPYHQ_API_URL
//	SUPPYHQ_CLIENT_ID
//	SUPPYHQ_CLIENT_SECRET
package main

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

//go:embed skills/suppyhq/SKILL.md
var skillMarkdown string

// Version is set by goreleaser at build time via -ldflags.
var Version = "dev"

const (
	defaultAPIURL = "https://app.suppyhq.com"
	configRel     = ".suppyhq/config.json"
)

// skillTargets maps an agent name to its on-disk SKILL.md location.
// All paths but `cursor` are user-scoped (under $HOME). Cursor only reads
// project-scoped skills from .cursor/skills/, so we install into the
// current working directory and print a note about it.
var skillTargets = []skillTarget{
	{name: "claude", relPath: ".claude/skills/suppyhq/SKILL.md", scope: "user"},
	{name: "codex", relPath: ".codex/skills/suppyhq/SKILL.md", scope: "user"},
	{name: "opencode", relPath: ".config/opencode/skills/suppyhq/SKILL.md", scope: "user"},
	{name: "cursor", relPath: ".cursor/skills/suppyhq/SKILL.md", scope: "project"},
}

type skillTarget struct {
	name    string
	relPath string
	scope   string // "user" → under $HOME; "project" → under cwd
}

func (s skillTarget) absPath() (string, error) {
	if s.scope == "project" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		return filepath.Join(cwd, s.relPath), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, s.relPath), nil
}

func findSkillTarget(name string) (skillTarget, bool) {
	for _, t := range skillTargets {
		if t.name == name {
			return t, true
		}
	}
	return skillTarget{}, false
}

type config struct {
	APIURL       string `json:"api_url"`
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		usage(stdout)
		return 1
	}
	cmd, rest := args[0], args[1:]

	switch cmd {
	case "help", "-h", "--help":
		usage(stdout)
		return 0
	case "version", "-v", "--version":
		fmt.Fprintln(stdout, Version)
		return 0
	case "auth":
		return runAuth(rest, stdin, stdout, stderr)
	case "install-skill":
		if err := runInstallSkill(rest, stdout); err != nil {
			fmt.Fprintf(stderr, "suppyhq: %v\n", err)
			return 1
		}
		return 0
	case "upgrade":
		if err := runUpgrade(stdout); err != nil {
			fmt.Fprintf(stderr, "suppyhq: %v\n", err)
			return 1
		}
		return 0
	}

	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintf(stderr, "suppyhq: config: %v\n", err)
		return 1
	}
	if cfg.ClientID == "" || cfg.ClientSecret == "" {
		fmt.Fprintln(stderr, "suppyhq: not authenticated. Run: suppyhq auth login")
		return 1
	}
	token, err := fetchToken(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "suppyhq: token: %v\n", err)
		return 1
	}

	switch cmd {
	case "inbox":
		body, err := apiGET(cfg, token, "/api/v1/conversations")
		if err != nil {
			fmt.Fprintf(stderr, "suppyhq: %v\n", err)
			return 1
		}
		printJSON(stdout, body)
	case "thread":
		if len(rest) < 1 {
			fmt.Fprintln(stderr, "suppyhq: thread: missing conversation id")
			return 1
		}
		body, err := apiGET(cfg, token, "/api/v1/conversations/"+rest[0])
		if err != nil {
			fmt.Fprintf(stderr, "suppyhq: %v\n", err)
			return 1
		}
		printJSON(stdout, body)
	case "customers":
		body, err := apiGET(cfg, token, "/api/v1/customers")
		if err != nil {
			fmt.Fprintf(stderr, "suppyhq: %v\n", err)
			return 1
		}
		printJSON(stdout, body)
	case "reply":
		if len(rest) < 1 {
			fmt.Fprintln(stderr, "suppyhq: reply: usage — suppyhq reply <conversation_id> <html_body>  (or pipe body to stdin)")
			return 1
		}
		// Strip --draft from positional args before reading the body.
		positional, draft := splitDraftFlag(rest)
		if len(positional) < 1 {
			fmt.Fprintln(stderr, "suppyhq: reply: missing conversation id")
			return 1
		}
		bodyHTML := readReplyBody(positional, stdin)
		if bodyHTML == "" {
			fmt.Fprintln(stderr, "suppyhq: reply: empty body")
			return 1
		}
		form := url.Values{"body_html": {bodyHTML}}
		if draft {
			form.Set("draft", "true")
		}
		body, err := apiPOST(cfg, token, "/api/v1/conversations/"+positional[0]+"/messages", form)
		if err != nil {
			fmt.Fprintf(stderr, "suppyhq: %v\n", err)
			return 1
		}
		printJSON(stdout, body)
	default:
		fmt.Fprintf(stderr, "suppyhq: unknown command: %s\n", cmd)
		return 1
	}
	return 0
}

// splitDraftFlag pulls --draft (or -d) out of the args, returning the rest
// and a boolean. Keeps the positional ordering of the conversation id and
// inline body intact so callers don't have to think about flag position.
func splitDraftFlag(args []string) (rest []string, draft bool) {
	rest = make([]string, 0, len(args))
	for _, a := range args {
		if a == "--draft" || a == "-d" {
			draft = true
			continue
		}
		rest = append(rest, a)
	}
	return rest, draft
}

// readReplyBody returns the HTML body for `reply` from the second arg if
// provided, else from stdin. Lets agents pipe content (`echo ... | reply <id>`)
// without escaping it through a shell argument.
func readReplyBody(args []string, stdin io.Reader) string {
	if len(args) >= 2 && strings.TrimSpace(args[1]) != "" {
		return args[1]
	}
	data, _ := io.ReadAll(stdin)
	return strings.TrimSpace(string(data))
}

func runAuth(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		fmt.Fprintln(stderr, "suppyhq: auth: usage — suppyhq auth (login | status | logout)")
		return 1
	}
	var err error
	switch args[0] {
	case "login":
		err = runAuthLogin(stdin, stdout)
	case "status":
		err = runAuthStatus(stdout)
	case "logout":
		err = runAuthLogout(stdout)
	default:
		fmt.Fprintf(stderr, "suppyhq: unknown auth subcommand: %s\n", args[0])
		return 1
	}
	if err != nil {
		fmt.Fprintf(stderr, "suppyhq: %v\n", err)
		return 1
	}
	return 0
}

func runAuthLogin(stdin io.Reader, stdout io.Writer) error {
	fmt.Fprintln(stdout, "suppyhq auth login")
	fmt.Fprintln(stdout)

	existing, _ := loadConfig()
	apiURL := promptDefault(stdin, stdout, "API URL", existing.APIURL, defaultAPIURL)

	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "To get credentials:")
	fmt.Fprintf(stdout, "  1. Open %s/agents in your browser\n", apiURL)
	fmt.Fprintln(stdout, `  2. Click "Add an agent", give it a name, click "Add agent"`)
	fmt.Fprintln(stdout, "  3. Copy the Client ID and Client Secret shown on the next screen")
	fmt.Fprintln(stdout)

	clientID := prompt(stdin, stdout, "Client ID")
	clientSecret := prompt(stdin, stdout, "Client Secret")

	cfg := &config{APIURL: apiURL, ClientID: clientID, ClientSecret: clientSecret}

	fmt.Fprintln(stdout)
	fmt.Fprint(stdout, "Testing connection… ")
	if _, err := fetchToken(cfg); err != nil {
		fmt.Fprintln(stdout, "failed.")
		return fmt.Errorf("the credentials didn't authenticate: %w", err)
	}
	fmt.Fprintln(stdout, "ok.")

	if err := saveConfig(cfg); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Saved to %s\n", configPath())
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "Next: install the Claude Code skill so AI can drive your inbox.")
	fmt.Fprintln(stdout, "  suppyhq install-skill")
	return nil
}

func runAuthStatus(stdout io.Writer) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	if cfg.ClientID == "" || cfg.ClientSecret == "" {
		fmt.Fprintln(stdout, "Not authenticated. Run: suppyhq auth login")
		return nil
	}
	if _, err := fetchToken(cfg); err != nil {
		return fmt.Errorf("authenticated to %s but the token endpoint rejected the credentials: %w", cfg.APIURL, err)
	}
	fmt.Fprintf(stdout, "Authenticated to %s as client %s\n", cfg.APIURL, cfg.ClientID)
	return nil
}

func runAuthLogout(stdout io.Writer) error {
	if _, err := os.Stat(configPath()); os.IsNotExist(err) {
		fmt.Fprintln(stdout, "Already logged out.")
		return nil
	}
	if err := os.Remove(configPath()); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "Logged out.")
	return nil
}

// runInstallSkill writes the embedded SKILL.md into the right directory for
// each supported AI agent. Defaults to Claude Code; --target picks another
// agent or "all". Refuses to overwrite an existing file unless --force is
// passed — we don't want to clobber an operator's local edits.
//
// Target paths:
//
//	claude    ~/.claude/skills/suppyhq/SKILL.md
//	codex     ~/.codex/skills/suppyhq/SKILL.md
//	opencode  ~/.config/opencode/skills/suppyhq/SKILL.md
//	cursor    ./.cursor/skills/suppyhq/SKILL.md  (project-scoped — cwd)
func runInstallSkill(args []string, stdout io.Writer) error {
	force := false
	target := "claude"
	for _, a := range args {
		switch {
		case a == "--force" || a == "-f":
			force = true
		case strings.HasPrefix(a, "--target="):
			target = strings.TrimPrefix(a, "--target=")
		case a == "--help" || a == "-h":
			printInstallSkillHelp(stdout)
			return nil
		}
	}

	var targets []skillTarget
	if target == "all" {
		targets = skillTargets
	} else {
		t, ok := findSkillTarget(target)
		if !ok {
			return fmt.Errorf("unknown target %q. Valid: claude, codex, opencode, cursor, all", target)
		}
		targets = []skillTarget{t}
	}

	for _, t := range targets {
		if err := installOneTarget(t, force, stdout); err != nil {
			return fmt.Errorf("%s: %w", t.name, err)
		}
	}
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "Restart your AI agent session to pick up the new skill.")
	return nil
}

func installOneTarget(t skillTarget, force bool, stdout io.Writer) error {
	abs, err := t.absPath()
	if err != nil {
		return err
	}
	if _, err := os.Stat(abs); err == nil && !force {
		fmt.Fprintf(stdout, "[%s] already installed at %s — use --force to overwrite\n", t.name, abs)
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(abs, []byte(skillMarkdown), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "[%s] installed: %s\n", t.name, abs)
	if t.scope == "project" {
		fmt.Fprintf(stdout, "[%s] note: Cursor reads skills per-project. Re-run from each project directory you want it in.\n", t.name)
	}
	return nil
}

func printInstallSkillHelp(stdout io.Writer) {
	fmt.Fprintln(stdout, `suppyhq install-skill — install the SuppyHQ skill into your AI agent

  install-skill                       Claude Code (default)
  install-skill --target=cursor       Cursor (project-scoped — uses cwd)
  install-skill --target=codex        Codex CLI
  install-skill --target=opencode     OpenCode
  install-skill --target=all          All of the above

  --force, -f                         Overwrite an existing local copy.

The skill ships embedded in this binary; no network call is made.`)
}

// fetchToken exchanges the configured client credentials for a short-lived
// Bearer access token via OAuth2 client-credentials grant.
func fetchToken(cfg *config) (string, error) {
	resp, err := http.PostForm(cfg.APIURL+"/oauth/token", url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {cfg.ClientID},
		"client_secret": {cfg.ClientSecret},
		"scope":         {"read reply"},
	})
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("token endpoint returned %d: %s", resp.StatusCode, string(body))
	}
	var tokenResp struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return "", err
	}
	if tokenResp.AccessToken == "" {
		return "", fmt.Errorf("no access_token in response: %s", string(body))
	}
	return tokenResp.AccessToken, nil
}

func apiGET(cfg *config, token, path string) ([]byte, error) {
	return apiRequest(cfg, token, "GET", path, nil)
}

func apiPOST(cfg *config, token, path string, form url.Values) ([]byte, error) {
	return apiRequest(cfg, token, "POST", path, form)
}

func apiRequest(cfg *config, token, method, path string, form url.Values) ([]byte, error) {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequest(method, cfg.APIURL+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(respBody))
	}
	return respBody, nil
}

func loadConfig() (*config, error) {
	cfg := &config{
		APIURL:       defaultAPIURL,
		ClientID:     os.Getenv("SUPPYHQ_CLIENT_ID"),
		ClientSecret: os.Getenv("SUPPYHQ_CLIENT_SECRET"),
	}
	if envURL := os.Getenv("SUPPYHQ_API_URL"); envURL != "" {
		cfg.APIURL = envURL
	}
	data, err := os.ReadFile(configPath())
	if err == nil {
		var disk config
		if err := json.Unmarshal(data, &disk); err != nil {
			return nil, err
		}
		// Env overrides on-disk; on-disk overrides defaults.
		if cfg.ClientID == "" {
			cfg.ClientID = disk.ClientID
		}
		if cfg.ClientSecret == "" {
			cfg.ClientSecret = disk.ClientSecret
		}
		if disk.APIURL != "" && os.Getenv("SUPPYHQ_API_URL") == "" {
			cfg.APIURL = disk.APIURL
		}
	}
	return cfg, nil
}

func saveConfig(cfg *config) error {
	if err := os.MkdirAll(filepath.Dir(configPath()), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(configPath(), data, 0o600)
}

func configPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, configRel)
}

func prompt(stdin io.Reader, stdout io.Writer, label string) string {
	fmt.Fprintf(stdout, "%s: ", label)
	r := bufio.NewReader(stdin)
	line, _ := r.ReadString('\n')
	return strings.TrimSpace(line)
}

func promptDefault(stdin io.Reader, stdout io.Writer, label, current, fallback string) string {
	def := current
	if def == "" {
		def = fallback
	}
	fmt.Fprintf(stdout, "%s [%s]: ", label, def)
	r := bufio.NewReader(stdin)
	line, _ := r.ReadString('\n')
	v := strings.TrimSpace(line)
	if v == "" {
		return def
	}
	return v
}

func printJSON(stdout io.Writer, raw []byte) {
	var pretty any
	if err := json.Unmarshal(raw, &pretty); err != nil {
		fmt.Fprintln(stdout, string(raw))
		return
	}
	out, _ := json.MarshalIndent(pretty, "", "  ")
	fmt.Fprintln(stdout, string(out))
}

// runUpgrade resolves the latest GitHub release, downloads the matching
// platform archive, verifies its SHA256 against the published
// checksums.txt, and replaces the running binary in place. Same shape
// as install.sh but via Go stdlib so a one-shot `suppyhq upgrade` does
// not depend on curl/tar/sha256sum being installed.
func runUpgrade(stdout io.Writer) error {
	const repo = "karloscodes/suppyhq-cli"

	fmt.Fprintln(stdout, "Checking for updates…")

	tag, err := latestReleaseTag(repo)
	if err != nil {
		return fmt.Errorf("could not check latest release: %w", err)
	}

	if tag == Version || strings.TrimPrefix(tag, "v") == strings.TrimPrefix(Version, "v") {
		fmt.Fprintf(stdout, "Already on %s.\n", tag)
		return nil
	}

	osName := runtime.GOOS
	arch := runtime.GOARCH
	if osName != "darwin" && osName != "linux" {
		return fmt.Errorf("unsupported OS %q (need darwin or linux)", osName)
	}
	if arch != "amd64" && arch != "arm64" {
		return fmt.Errorf("unsupported arch %q (need amd64 or arm64)", arch)
	}

	versionNoV := strings.TrimPrefix(tag, "v")
	archive := fmt.Sprintf("suppyhq_%s_%s_%s.tar.gz", versionNoV, osName, arch)
	archiveURL := fmt.Sprintf("https://github.com/%s/releases/download/%s/%s", repo, tag, archive)
	checksumsURL := fmt.Sprintf("https://github.com/%s/releases/download/%s/checksums.txt", repo, tag)

	fmt.Fprintf(stdout, "Downloading %s…\n", archive)

	tmpDir, err := os.MkdirTemp("", "suppyhq-upgrade-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)

	archivePath := filepath.Join(tmpDir, archive)
	if err := downloadFile(archiveURL, archivePath); err != nil {
		return fmt.Errorf("download: %w", err)
	}

	fmt.Fprintln(stdout, "Verifying checksum…")
	if err := verifyChecksum(archivePath, checksumsURL, archive); err != nil {
		return fmt.Errorf("checksum mismatch: %w", err)
	}

	if err := extractTarGzBinary(archivePath, "suppyhq", filepath.Join(tmpDir, "suppyhq")); err != nil {
		return fmt.Errorf("extract: %w", err)
	}

	self, err := os.Executable()
	if err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(self); err == nil {
		self = resolved
	}

	if err := replaceBinary(self, filepath.Join(tmpDir, "suppyhq")); err != nil {
		return fmt.Errorf("replace %s: %w", self, err)
	}

	fmt.Fprintf(stdout, "Upgraded to %s. Run `suppyhq version` to confirm.\n", tag)
	return nil
}

func latestReleaseTag(repo string) (string, error) {
	resp, err := http.Get("https://api.github.com/repos/" + repo + "/releases/latest")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("github API returned %d: %s", resp.StatusCode, string(body))
	}
	var rel struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return "", err
	}
	if rel.TagName == "" {
		return "", fmt.Errorf("no tag_name in release response")
	}
	return rel.TagName, nil
}

func downloadFile(url, dest string) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP %d for %s", resp.StatusCode, url)
	}
	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, resp.Body)
	return err
}

func verifyChecksum(archivePath, checksumsURL, expectedName string) error {
	resp, err := http.Get(checksumsURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP %d for %s", resp.StatusCode, checksumsURL)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	// checksums.txt format: "<sha256>  <filename>" per line.
	var expected string
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == expectedName {
			expected = fields[0]
			break
		}
	}
	if expected == "" {
		return fmt.Errorf("no checksum entry for %s", expectedName)
	}

	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	actual := hex.EncodeToString(h.Sum(nil))
	if actual != expected {
		return fmt.Errorf("expected %s, got %s", expected, actual)
	}
	return nil
}

func extractTarGzBinary(archivePath, wantName, destPath string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if filepath.Base(hdr.Name) != wantName || hdr.Typeflag != tar.TypeReg {
			continue
		}
		out, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, tr); err != nil {
			out.Close()
			return err
		}
		out.Close()
		return nil
	}
	return fmt.Errorf("%s not found in archive", wantName)
}

// replaceBinary swaps the running executable with the new one. We copy
// the new binary into a temp file in the *same directory* as the target
// so the rename is atomic on the same filesystem; otherwise os.Rename
// would fall apart across mounts. On Unix, replacing the inode of a
// running binary is safe — the kernel keeps the old inode alive for the
// current process, while new invocations pick up the new file.
func replaceBinary(target, source string) error {
	dir := filepath.Dir(target)
	tmp, err := os.CreateTemp(dir, ".suppyhq-upgrade-")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			os.Remove(tmpPath)
		}
	}()

	src, err := os.Open(source)
	if err != nil {
		tmp.Close()
		return err
	}
	if _, err := io.Copy(tmp, src); err != nil {
		src.Close()
		tmp.Close()
		return err
	}
	src.Close()
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, 0o755); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, target); err != nil {
		return err
	}
	cleanup = false
	return nil
}

func usage(stdout io.Writer) {
	fmt.Fprintln(stdout, `suppyhq — official CLI for SuppyHQ

Auth:
  auth login                    Interactive setup. Run this once.
  auth status                   Show who's authenticated.
  auth logout                   Forget credentials.

Skill:
  install-skill                 Install the Claude Code skill into ~/.claude/skills/suppyhq.
  install-skill --force         Overwrite an existing local copy.

Self:
  upgrade                       Pull the latest release from GitHub and replace this binary.

Read:
  inbox                         List conversations.
  thread <id>                   Show one conversation with messages.
  customers                     List customers.

Write:
  reply <id> <html_body>        Post a reply (queued for delayed send).
  reply <id>                    Same, body read from stdin.
  reply <id> --draft            Save as a draft for the operator to send manually.

Output: JSON. Pipe to jq, or feed straight to an LLM.

Configuration:
  ~/.suppyhq/config.json    or    SUPPYHQ_API_URL / SUPPYHQ_CLIENT_ID / SUPPYHQ_CLIENT_SECRET`)
}
