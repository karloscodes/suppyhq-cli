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
	"context"
	"crypto/rand"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
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
	ClientID     string `json:"client_id,omitempty"`
	ClientSecret string `json:"client_secret,omitempty"`
	// AccessToken is set by the browser OAuth flow. When present, it's
	// used directly as the Bearer token, skipping client_credentials
	// exchange entirely. Empty for --manual / paste-credentials mode,
	// which keeps the older client_id+secret flow.
	AccessToken string `json:"access_token,omitempty"`
	// AgentName is just for display by `auth status`.
	AgentName string `json:"agent_name,omitempty"`
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

	// Hint about a newer release if one exists. Cached for 24h so we
	// don't hit GitHub on every invocation; skipped for the meta
	// commands (version, help, upgrade) and for `dev` builds. Set
	// SUPPYHQ_NO_VERSION_CHECK=1 to silence entirely.
	if shouldCheckVersion(cmd) {
		refreshLatestVersion()
		defer maybeShowUpgradeNotice(stderr)
	}

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
	// Authenticated by either the browser OAuth flow (AccessToken set)
	// or the manual client_credentials path (ClientID + ClientSecret).
	if cfg.AccessToken == "" && (cfg.ClientID == "" || cfg.ClientSecret == "") {
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
		err = runAuthLogin(args[1:], stdin, stdout)
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

// runAuthLogin dispatches between the browser OAuth flow (default) and
// the paste-credentials fallback (--manual). The browser path is the
// recommended one — no token transits the operator's clipboard, shell
// history, or any AI agent's context window.
func runAuthLogin(args []string, stdin io.Reader, stdout io.Writer) error {
	manual := false
	name := ""
	apiURLFlag := ""
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--manual":
			manual = true
		case strings.HasPrefix(a, "--name="):
			name = strings.TrimPrefix(a, "--name=")
		case strings.HasPrefix(a, "--api-url="):
			apiURLFlag = strings.TrimPrefix(a, "--api-url=")
		}
	}

	if manual {
		return runAuthLoginManual(stdin, stdout, apiURLFlag)
	}
	return runAuthLoginBrowser(stdin, stdout, name, apiURLFlag)
}

// runAuthLoginBrowser is the default. PKCE + loopback redirect, modeled
// on basecamp + gumroad. The token only ever lives between the server
// and the CLI process; never in the clipboard, shell history, or an
// AI's context window.
func runAuthLoginBrowser(stdin io.Reader, stdout io.Writer, name, apiURLFlag string) error {
	fmt.Fprintln(stdout, "suppyhq auth login")
	fmt.Fprintln(stdout)

	existing, _ := loadConfig()
	apiURL := apiURLFlag
	if apiURL == "" {
		apiURL = promptDefault(stdin, stdout, "API URL", existing.APIURL, defaultAPIURL)
	}
	if name == "" {
		hostname, _ := os.Hostname()
		fallback := "suppyhq-cli"
		if hostname != "" {
			fallback = "suppyhq-cli on " + hostname
		}
		name = promptDefault(stdin, stdout, "Agent name", "", fallback)
	}

	verifier, challenge, err := generatePKCE()
	if err != nil {
		return fmt.Errorf("pkce: %w", err)
	}
	state, err := randomString(16)
	if err != nil {
		return fmt.Errorf("state: %w", err)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("loopback listener: %w", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	redirectURI := fmt.Sprintf("http://127.0.0.1:%d/cb", port)

	authURL := buildCliAuthorizeURL(apiURL, name, challenge, redirectURI, state)

	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "Opening your browser to approve the CLI…")
	fmt.Fprintf(stdout, "If it doesn't open, visit:\n  %s\n", authURL)
	_ = openBrowser(authURL)

	code, returnedClientID, err := waitForCallback(listener, state, 5*time.Minute, stdout)
	if err != nil {
		return err
	}

	fmt.Fprintln(stdout)
	fmt.Fprint(stdout, "Exchanging authorization code… ")
	token, err := exchangeCodeForToken(apiURL, code, verifier, redirectURI, returnedClientID)
	if err != nil {
		fmt.Fprintln(stdout, "failed.")
		return fmt.Errorf("token exchange: %w", err)
	}
	fmt.Fprintln(stdout, "ok.")

	cfg := &config{
		APIURL:      apiURL,
		ClientID:    returnedClientID,
		AccessToken: token,
		AgentName:   name,
	}
	if err := saveConfig(cfg); err != nil {
		return err
	}

	fmt.Fprintf(stdout, "Saved to %s\n", configPath())
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "Done. Restart your AI agent so the skill loads, then ask it about your inbox.")
	return nil
}

// runAuthLoginManual is the original paste flow, kept as a fallback for
// CI, ssh-only boxes, and scripted installs. Behind --manual.
func runAuthLoginManual(stdin io.Reader, stdout io.Writer, apiURLFlag string) error {
	fmt.Fprintln(stdout, "suppyhq auth login --manual")
	fmt.Fprintln(stdout)

	existing, _ := loadConfig()
	apiURL := apiURLFlag
	if apiURL == "" {
		apiURL = promptDefault(stdin, stdout, "API URL", existing.APIURL, defaultAPIURL)
	}

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

// generatePKCE returns a verifier + challenge per RFC 7636. Verifier is
// 32 random bytes base64url-encoded (43 chars after padding strip);
// challenge is base64url(sha256(verifier)).
func generatePKCE() (verifier, challenge string, err error) {
	raw := make([]byte, 32)
	if _, err = rand.Read(raw); err != nil {
		return
	}
	verifier = base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	return
}

func randomString(n int) (string, error) {
	raw := make([]byte, n)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func buildCliAuthorizeURL(apiURL, name, challenge, redirectURI, state string) string {
	q := url.Values{
		"name":                  {name},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"redirect_uri":          {redirectURI},
		"scope":                 {"read reply"},
		"state":                 {state},
	}
	return strings.TrimRight(apiURL, "/") + "/cli_authorization/new?" + q.Encode()
}

// waitForCallback runs a one-shot HTTP server that captures the OAuth
// callback at /cb. Validates `state`, returns the code + client_id sent
// by the server. Times out so the CLI doesn't hang forever if the
// operator closes the browser tab.
func waitForCallback(listener net.Listener, expectedState string, timeout time.Duration, stdout io.Writer) (code, clientID string, err error) {
	type result struct {
		code, clientID string
		err            error
	}
	ch := make(chan result, 1)

	mux := http.NewServeMux()
	mux.HandleFunc("/cb", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if errParam := q.Get("error"); errParam != "" {
			browserMsg, cliMsg := authErrorMessages(errParam, q.Get("error_description"))
			renderCallbackError(w, browserMsg)
			ch <- result{err: fmt.Errorf("%s", cliMsg)}
			return
		}
		if q.Get("state") != expectedState {
			renderCallbackError(w, "State mismatch — refusing to continue.")
			ch <- result{err: fmt.Errorf("state mismatch")}
			return
		}
		c := q.Get("code")
		cid := q.Get("client_id")
		if c == "" || cid == "" {
			renderCallbackError(w, "Missing code or client_id in callback.")
			ch <- result{err: fmt.Errorf("missing code or client_id in callback")}
			return
		}
		renderCallbackOK(w)
		ch <- result{code: c, clientID: cid}
	})

	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(listener) }()
	defer srv.Shutdown(context.Background())

	select {
	case r := <-ch:
		return r.code, r.clientID, r.err
	case <-time.After(timeout):
		return "", "", fmt.Errorf("timed out after %s waiting for browser approval", timeout)
	}
}

func renderCallbackOK(w http.ResponseWriter) {
	body := `<!doctype html><meta charset=utf-8><title>Authorized</title>
<style>body{font:15px/1.5 -apple-system,BlinkMacSystemFont,sans-serif;color:#1c1917;background:#f5f5f4;margin:0;display:grid;place-items:center;min-height:100vh}div{background:#fff;padding:40px;border-radius:14px;box-shadow:0 24px 64px -16px rgba(0,0,0,.1);max-width:420px;text-align:center}h1{margin:0 0 8px;font-size:18px}p{margin:0;color:#57534e;font-size:14px}</style>
<div><h1>Authorized.</h1><p>You can close this tab and return to your terminal.</p></div>`
	writeHTML(w, 200, body)
}

func renderCallbackError(w http.ResponseWriter, msg string) {
	body := fmt.Sprintf(`<!doctype html><meta charset=utf-8><title>Authorization failed</title>
<style>body{font:15px/1.5 -apple-system,BlinkMacSystemFont,sans-serif;color:#1c1917;background:#f5f5f4;margin:0;display:grid;place-items:center;min-height:100vh}div{background:#fff;padding:40px;border-radius:14px;box-shadow:0 24px 64px -16px rgba(0,0,0,.1);max-width:420px;text-align:center}h1{margin:0 0 8px;font-size:18px}p{margin:0;color:#dc2626;font-size:14px}</style>
<div><h1>Authorization failed</h1><p>%s</p></div>`, msg)
	writeHTML(w, 400, body)
}

// authErrorMessages turns an OAuth2 error code into (browserMsg, cliMsg).
// access_denied is the operator clicking Cancel — friendly tone. Everything
// else is treated as a real failure.
func authErrorMessages(code, description string) (browserMsg, cliMsg string) {
	switch code {
	case "access_denied":
		return "Authorization cancelled. You can close this tab.", "authorization cancelled"
	default:
		msg := description
		if msg == "" {
			msg = code
		}
		return "Authorization failed: " + msg, "authorization rejected: " + code
	}
}

// writeHTML writes the body with explicit Content-Length and a flush so
// the browser doesn't show a blank page when the server immediately
// shuts down after handling the callback. Without the flush the response
// can be cut short before the bytes leave the kernel buffer.
func writeHTML(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(body)))
	w.Header().Set("Connection", "close")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

func exchangeCodeForToken(apiURL, code, verifier, redirectURI, clientID string) (string, error) {
	resp, err := http.PostForm(strings.TrimRight(apiURL, "/")+"/oauth/token", url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"client_id":     {clientID},
		"code_verifier": {verifier},
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

// openBrowser is a best-effort browser launch. Failure is non-fatal —
// the auth URL is also printed so the operator can copy it manually.
func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		return fmt.Errorf("unsupported OS %q", runtime.GOOS)
	}
	return cmd.Start()
}

func runAuthStatus(stdout io.Writer) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	if cfg.AccessToken == "" && (cfg.ClientID == "" || cfg.ClientSecret == "") {
		fmt.Fprintln(stdout, "Not authenticated. Run: suppyhq auth login")
		return nil
	}
	if _, err := fetchToken(cfg); err != nil {
		return fmt.Errorf("authenticated to %s but the token endpoint rejected the credentials: %w", cfg.APIURL, err)
	}
	if cfg.AgentName != "" {
		fmt.Fprintf(stdout, "Authenticated to %s as %q (client %s)\n", cfg.APIURL, cfg.AgentName, cfg.ClientID)
	} else {
		fmt.Fprintf(stdout, "Authenticated to %s as client %s\n", cfg.APIURL, cfg.ClientID)
	}
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

// fetchToken returns the Bearer token for API calls. If the config has
// an access_token (set by the browser OAuth flow), use it directly —
// no extra round trip. Otherwise fall back to client_credentials,
// which is what `auth login --manual` configures.
func fetchToken(cfg *config) (string, error) {
	if cfg.AccessToken != "" {
		return cfg.AccessToken, nil
	}
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
		if cfg.AccessToken == "" {
			cfg.AccessToken = disk.AccessToken
		}
		if cfg.AgentName == "" {
			cfg.AgentName = disk.AgentName
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

// versionCheckTTL bounds how often we hit the GitHub API. A day is
// enough — releases happen on the order of weeks, the hint is
// best-effort, and there's an explicit `suppyhq upgrade` for anyone in
// a hurry.
const versionCheckTTL = 24 * time.Hour

type versionCheck struct {
	CheckedAt time.Time `json:"checked_at"`
	Latest    string    `json:"latest"`
}

func versionCheckPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".suppyhq", "version_check.json")
}

func readVersionCheck() *versionCheck {
	data, err := os.ReadFile(versionCheckPath())
	if err != nil {
		return nil
	}
	var vc versionCheck
	if json.Unmarshal(data, &vc) != nil {
		return nil
	}
	return &vc
}

func writeVersionCheck(vc *versionCheck) {
	if err := os.MkdirAll(filepath.Dir(versionCheckPath()), 0o700); err != nil {
		return
	}
	data, err := json.Marshal(vc)
	if err != nil {
		return
	}
	_ = os.WriteFile(versionCheckPath(), data, 0o600)
}

func shouldCheckVersion(cmd string) bool {
	if os.Getenv("SUPPYHQ_NO_VERSION_CHECK") != "" {
		return false
	}
	if Version == "dev" {
		return false
	}
	switch cmd {
	case "version", "-v", "--version",
		"help", "-h", "--help",
		"upgrade":
		return false
	}
	return true
}

// refreshLatestVersion bumps the cache if it's stale. Bounded by a
// short timeout so a flaky network can't wedge a fast command.
// Failures are remembered (we still write CheckedAt) so we don't retry
// every invocation.
func refreshLatestVersion() {
	vc := readVersionCheck()
	if vc != nil && time.Since(vc.CheckedAt) < versionCheckTTL {
		return
	}

	stale := &versionCheck{CheckedAt: time.Now()}
	if vc != nil {
		stale.Latest = vc.Latest
	}

	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("https://api.github.com/repos/karloscodes/suppyhq-cli/releases/latest")
	if err != nil {
		writeVersionCheck(stale)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		writeVersionCheck(stale)
		return
	}
	var rel struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil || rel.TagName == "" {
		writeVersionCheck(stale)
		return
	}
	writeVersionCheck(&versionCheck{CheckedAt: time.Now(), Latest: rel.TagName})
}

// maybeShowUpgradeNotice prints a dim one-liner on stderr when a newer
// release is cached. Stderr keeps it out of `jq` pipelines and other
// stdout-consumers.
func maybeShowUpgradeNotice(stderr io.Writer) {
	vc := readVersionCheck()
	if vc == nil || vc.Latest == "" {
		return
	}
	if !isNewerVersion(vc.Latest, Version) {
		return
	}
	fmt.Fprintf(stderr,
		"\n\033[2mA newer suppyhq is available: %s → %s\nRun `suppyhq upgrade` to update.\033[0m\n",
		Version, vc.Latest)
}

// isNewerVersion compares two "vMAJOR.MINOR.PATCH" strings.
// Pre-release suffixes (-rc1, -beta) are stripped — we don't show
// notices for those.
func isNewerVersion(latest, current string) bool {
	return semverInt(latest) > semverInt(current)
}

func semverInt(s string) int64 {
	s = strings.TrimPrefix(s, "v")
	if i := strings.IndexAny(s, "-+"); i >= 0 {
		s = s[:i]
	}
	parts := strings.Split(s, ".")
	var v int64
	for i := 0; i < 3; i++ {
		v *= 10000
		if i < len(parts) {
			n, _ := strconv.Atoi(parts[i])
			v += int64(n)
		}
	}
	return v
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
  auth login                    Browser-based OAuth flow (default). Opens your
                                browser, you click Allow, the token lands in
                                ~/.suppyhq/config.json without ever touching
                                your clipboard or this AI's context window.
  auth login --name "Claude"    Name the agent that gets created.
  auth login --manual           Fallback: paste a Client ID + Secret created
                                via app.suppyhq.com/agents.
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
