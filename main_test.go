package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRun_Help(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"--help"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("want 0, got %d (stderr=%s)", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "official CLI") {
		t.Errorf("usage text missing: %s", stdout.String())
	}
}

func TestRun_Version(t *testing.T) {
	var stdout bytes.Buffer
	code := run([]string{"version"}, nil, &stdout, io.Discard)
	if code != 0 {
		t.Fatalf("got %d", code)
	}
	if strings.TrimSpace(stdout.String()) != Version {
		t.Errorf("want %q, got %q", Version, stdout.String())
	}
}

func TestRun_NotAuthenticated(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SUPPYHQ_CLIENT_ID", "")
	t.Setenv("SUPPYHQ_CLIENT_SECRET", "")

	var stderr bytes.Buffer
	code := run([]string{"inbox"}, nil, io.Discard, &stderr)
	if code != 1 {
		t.Fatalf("want 1, got %d", code)
	}
	if !strings.Contains(stderr.String(), "auth login") {
		t.Errorf("expected 'auth login' hint: %s", stderr.String())
	}
}

func TestRun_UnknownCommand(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SUPPYHQ_CLIENT_ID", "id")
	t.Setenv("SUPPYHQ_CLIENT_SECRET", "secret")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"access_token":"tok"}`))
	}))
	defer srv.Close()
	t.Setenv("SUPPYHQ_API_URL", srv.URL)

	var stderr bytes.Buffer
	code := run([]string{"floob"}, nil, io.Discard, &stderr)
	if code != 1 {
		t.Fatalf("want 1, got %d", code)
	}
	if !strings.Contains(stderr.String(), "unknown command") {
		t.Errorf("expected 'unknown command': %s", stderr.String())
	}
}

func TestFetchToken_Happy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/token" {
			t.Errorf("bad path: %s", r.URL.Path)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if got := r.PostForm.Get("grant_type"); got != "client_credentials" {
			t.Errorf("grant_type: want client_credentials, got %s", got)
		}
		if r.PostForm.Get("client_id") != "id" {
			t.Errorf("client_id: %s", r.PostForm.Get("client_id"))
		}
		w.Write([]byte(`{"access_token":"abc123","token_type":"Bearer"}`))
	}))
	defer srv.Close()

	tok, err := fetchToken(&config{APIURL: srv.URL, ClientID: "id", ClientSecret: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	if tok != "abc123" {
		t.Errorf("got %q", tok)
	}
}

func TestFetchToken_Rejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"invalid_client"}`))
	}))
	defer srv.Close()

	_, err := fetchToken(&config{APIURL: srv.URL, ClientID: "x", ClientSecret: "y"})
	if err == nil {
		t.Fatal("want error")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("missing 401 in error: %v", err)
	}
}

func TestFetchToken_EmptyAccessToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	_, err := fetchToken(&config{APIURL: srv.URL, ClientID: "x", ClientSecret: "y"})
	if err == nil {
		t.Fatal("want error for empty token")
	}
}

func TestApiGET_Happy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			t.Errorf("auth header: %s", got)
		}
		w.Write([]byte(`[{"id":1}]`))
	}))
	defer srv.Close()

	body, err := apiGET(&config{APIURL: srv.URL}, "tok", "/api/v1/conversations")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"id":1`) {
		t.Errorf("bad body: %s", body)
	}
}

func TestApiGET_500(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("boom"))
	}))
	defer srv.Close()

	_, err := apiGET(&config{APIURL: srv.URL}, "tok", "/x")
	if err == nil || !strings.Contains(err.Error(), "500") {
		t.Errorf("want 500 error, got %v", err)
	}
}

func TestApiPOST_FormBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.PostForm.Get("body_html") != "<p>hi</p>" {
			t.Errorf("body_html: %v", r.PostForm)
		}
		if r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
			t.Errorf("content-type: %s", r.Header.Get("Content-Type"))
		}
		w.Write([]byte(`{"id":42}`))
	}))
	defer srv.Close()

	body, err := apiPOST(&config{APIURL: srv.URL}, "tok", "/x", url.Values{"body_html": {"<p>hi</p>"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "42") {
		t.Errorf("bad body: %s", body)
	}
}

func TestReadReplyBody_FromArg(t *testing.T) {
	got := readReplyBody([]string{"42", "<p>arg</p>"}, strings.NewReader("STDIN"))
	if got != "<p>arg</p>" {
		t.Errorf("want arg, got %q", got)
	}
}

func TestReadReplyBody_FromStdin(t *testing.T) {
	got := readReplyBody([]string{"42"}, strings.NewReader("  <p>from stdin</p>  \n"))
	if got != "<p>from stdin</p>" {
		t.Errorf("want trimmed stdin, got %q", got)
	}
}

func TestReadReplyBody_BlankArgFallsToStdin(t *testing.T) {
	// Blank string as 2nd arg shouldn't suppress stdin reading.
	got := readReplyBody([]string{"42", "   "}, strings.NewReader("<p>stdin</p>"))
	if got != "<p>stdin</p>" {
		t.Errorf("blank arg should fall through to stdin, got %q", got)
	}
}

func TestSaveLoadConfig_Roundtrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SUPPYHQ_API_URL", "")
	t.Setenv("SUPPYHQ_CLIENT_ID", "")
	t.Setenv("SUPPYHQ_CLIENT_SECRET", "")

	in := &config{APIURL: "https://example.test", ClientID: "id", ClientSecret: "secret"}
	if err := saveConfig(in); err != nil {
		t.Fatal(err)
	}

	out, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if out.APIURL != in.APIURL || out.ClientID != in.ClientID || out.ClientSecret != in.ClientSecret {
		t.Errorf("roundtrip mismatch: in=%+v out=%+v", in, out)
	}

	info, err := os.Stat(filepath.Join(home, configRel))
	if err != nil {
		t.Fatal(err)
	}
	// Config holds a secret — refuse to ship without restrictive perms.
	if info.Mode().Perm() != 0o600 {
		t.Errorf("want 0600, got %o", info.Mode().Perm())
	}
}

func TestLoadConfig_EnvOverridesDisk(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SUPPYHQ_API_URL", "")
	t.Setenv("SUPPYHQ_CLIENT_ID", "")
	t.Setenv("SUPPYHQ_CLIENT_SECRET", "")

	if err := saveConfig(&config{APIURL: "https://disk", ClientID: "disk_id", ClientSecret: "disk_secret"}); err != nil {
		t.Fatal(err)
	}

	t.Setenv("SUPPYHQ_API_URL", "https://env")
	t.Setenv("SUPPYHQ_CLIENT_ID", "env_id")
	t.Setenv("SUPPYHQ_CLIENT_SECRET", "env_secret")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.APIURL != "https://env" || cfg.ClientID != "env_id" || cfg.ClientSecret != "env_secret" {
		t.Errorf("env should override disk: %+v", cfg)
	}
}

func claudeTarget(t *testing.T) skillTarget {
	t.Helper()
	tt, ok := findSkillTarget("claude")
	if !ok {
		t.Fatal("claude target missing from skillTargets")
	}
	return tt
}

func TestInstallSkill_FreshInstall_DefaultsToClaude(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	var out bytes.Buffer
	if err := runInstallSkill(nil, &out); err != nil {
		t.Fatal(err)
	}

	target := filepath.Join(home, claudeTarget(t).relPath)
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "name: suppyhq") {
		t.Errorf("frontmatter missing in installed skill")
	}
	if !strings.Contains(out.String(), "[claude] installed:") {
		t.Errorf("missing claude install line: %s", out.String())
	}
}

func TestInstallSkill_NoOverwriteWithoutForce(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	target := filepath.Join(home, claudeTarget(t).relPath)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("LOCAL EDITS"), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := runInstallSkill(nil, &out); err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(target)
	if string(data) != "LOCAL EDITS" {
		t.Errorf("local edits clobbered: %s", data)
	}
	if !strings.Contains(out.String(), "--force") {
		t.Errorf("expected --force hint: %s", out.String())
	}
}

func TestInstallSkill_ForceOverwrites(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	target := filepath.Join(home, claudeTarget(t).relPath)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("LOCAL EDITS"), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := runInstallSkill([]string{"--force"}, &out); err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(target)
	if string(data) == "LOCAL EDITS" {
		t.Errorf("--force did not overwrite")
	}
	if !strings.Contains(string(data), "name: suppyhq") {
		t.Errorf("embedded skill missing after --force overwrite")
	}
}

func TestInstallSkill_PerTarget(t *testing.T) {
	cases := []struct {
		target  string
		wantRel string
	}{
		{"codex", ".codex/skills/suppyhq/SKILL.md"},
		{"opencode", ".config/opencode/skills/suppyhq/SKILL.md"},
	}

	for _, c := range cases {
		t.Run(c.target, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)

			var out bytes.Buffer
			if err := runInstallSkill([]string{"--target=" + c.target}, &out); err != nil {
				t.Fatal(err)
			}
			abs := filepath.Join(home, c.wantRel)
			data, err := os.ReadFile(abs)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(data), "name: suppyhq") {
				t.Errorf("frontmatter missing in %s", abs)
			}
			if !strings.Contains(out.String(), "["+c.target+"] installed:") {
				t.Errorf("missing per-target install line: %s", out.String())
			}
		})
	}
}

func TestInstallSkill_CursorIsProjectScoped(t *testing.T) {
	cwd := t.TempDir()
	if err := os.Chdir(cwd); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := runInstallSkill([]string{"--target=cursor"}, &out); err != nil {
		t.Fatal(err)
	}

	abs := filepath.Join(cwd, ".cursor/skills/suppyhq/SKILL.md")
	data, err := os.ReadFile(abs)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "name: suppyhq") {
		t.Errorf("frontmatter missing")
	}
	if !strings.Contains(out.String(), "per-project") {
		t.Errorf("expected per-project note for cursor: %s", out.String())
	}
}

func TestInstallSkill_TargetAll(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := t.TempDir()
	if err := os.Chdir(cwd); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := runInstallSkill([]string{"--target=all"}, &out); err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{
		filepath.Join(home, ".claude/skills/suppyhq/SKILL.md"),
		filepath.Join(home, ".codex/skills/suppyhq/SKILL.md"),
		filepath.Join(home, ".config/opencode/skills/suppyhq/SKILL.md"),
		filepath.Join(cwd, ".cursor/skills/suppyhq/SKILL.md"),
	} {
		if _, err := os.Stat(want); err != nil {
			t.Errorf("expected %s to exist: %v", want, err)
		}
	}
}

func TestInstallSkill_UnknownTarget(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	err := runInstallSkill([]string{"--target=floob"}, io.Discard)
	if err == nil {
		t.Fatal("want error for unknown target")
	}
	if !strings.Contains(err.Error(), "unknown target") {
		t.Errorf("missing 'unknown target': %v", err)
	}
}

func TestRun_InboxEndToEnd(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth/token":
			w.Write([]byte(`{"access_token":"tok"}`))
		case "/api/v1/conversations":
			if got := r.Header.Get("Authorization"); got != "Bearer tok" {
				t.Errorf("auth header: %s", got)
			}
			w.Write([]byte(`[{"id":1,"subject":"hi"}]`))
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SUPPYHQ_API_URL", "")
	t.Setenv("SUPPYHQ_CLIENT_ID", "")
	t.Setenv("SUPPYHQ_CLIENT_SECRET", "")
	if err := saveConfig(&config{APIURL: srv.URL, ClientID: "id", ClientSecret: "secret"}); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := run([]string{"inbox"}, nil, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("want 0, got %d (stderr=%s)", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"subject"`) {
		t.Errorf("missing subject in output: %s", stdout.String())
	}
}

func TestSemverInt_OrdersCorrectly(t *testing.T) {
	cases := []struct {
		a, b string
		want bool // a > b
	}{
		{"v0.2.0", "v0.1.0", true},
		{"v0.10.0", "v0.2.0", true}, // double-digit minor must beat lexical compare
		{"v1.0.0", "v0.99.99", true},
		{"v0.2.0", "v0.2.0", false},
		{"v0.2.0-rc1", "v0.2.0", false}, // rc strip → equal → not newer
		{"v0.2.1", "v0.2.0", true},
	}
	for _, c := range cases {
		got := semverInt(c.a) > semverInt(c.b)
		if got != c.want {
			t.Errorf("%s > %s: got %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestShouldCheckVersion_SkipsNoiseAndDev(t *testing.T) {
	t.Setenv("SUPPYHQ_NO_VERSION_CHECK", "")
	origVersion := Version
	defer func() { Version = origVersion }()

	Version = "v0.2.0"
	for _, cmd := range []string{"version", "-v", "--version", "help", "-h", "--help", "upgrade"} {
		if shouldCheckVersion(cmd) {
			t.Errorf("expected skip for %q", cmd)
		}
	}
	for _, cmd := range []string{"inbox", "thread", "customers", "reply", "auth", "install-skill"} {
		if !shouldCheckVersion(cmd) {
			t.Errorf("expected check for %q", cmd)
		}
	}

	Version = "dev"
	if shouldCheckVersion("inbox") {
		t.Error("dev builds should never trigger a version check")
	}
}

func TestShouldCheckVersion_RespectsEnvOptOut(t *testing.T) {
	t.Setenv("SUPPYHQ_NO_VERSION_CHECK", "1")
	origVersion := Version
	defer func() { Version = origVersion }()
	Version = "v0.2.0"

	if shouldCheckVersion("inbox") {
		t.Error("SUPPYHQ_NO_VERSION_CHECK=1 must silence the check")
	}
}

func TestVersionCheckRoundTrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if got := readVersionCheck(); got != nil {
		t.Errorf("expected nil for missing file, got %+v", got)
	}

	writeVersionCheck(&versionCheck{
		CheckedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		Latest:    "v0.2.1",
	})
	got := readVersionCheck()
	if got == nil {
		t.Fatal("expected cache, got nil")
	}
	if got.Latest != "v0.2.1" {
		t.Errorf("latest: %q", got.Latest)
	}
}

func TestMaybeShowUpgradeNotice_PrintsWhenBehind(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SUPPYHQ_NO_VERSION_CHECK", "")
	origVersion := Version
	defer func() { Version = origVersion }()
	Version = "v0.2.0"

	writeVersionCheck(&versionCheck{CheckedAt: time.Now(), Latest: "v0.2.1"})

	var stderr bytes.Buffer
	maybeShowUpgradeNotice(&stderr)
	if !strings.Contains(stderr.String(), "v0.2.0 → v0.2.1") {
		t.Errorf("expected upgrade notice, got: %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "suppyhq upgrade") {
		t.Errorf("expected suggested command, got: %q", stderr.String())
	}
}

func TestMaybeShowUpgradeNotice_QuietWhenCurrent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	origVersion := Version
	defer func() { Version = origVersion }()
	Version = "v0.2.1"

	writeVersionCheck(&versionCheck{CheckedAt: time.Now(), Latest: "v0.2.1"})

	var stderr bytes.Buffer
	maybeShowUpgradeNotice(&stderr)
	if stderr.Len() != 0 {
		t.Errorf("expected silence when current, got: %q", stderr.String())
	}
}

func TestGeneratePKCE(t *testing.T) {
	v1, c1, err := generatePKCE()
	if err != nil {
		t.Fatal(err)
	}
	if v1 == "" || c1 == "" {
		t.Fatal("verifier or challenge empty")
	}
	if v1 == c1 {
		t.Fatal("verifier and challenge must differ")
	}
	v2, c2, _ := generatePKCE()
	if v1 == v2 || c1 == c2 {
		t.Fatal("subsequent calls must produce different verifier/challenge")
	}
	// challenge = base64url(sha256(verifier))
	sum := sha256.Sum256([]byte(v1))
	want := base64.RawURLEncoding.EncodeToString(sum[:])
	if c1 != want {
		t.Errorf("challenge != base64url(sha256(verifier)); got %q want %q", c1, want)
	}
}

func TestBuildCliAuthorizeURL(t *testing.T) {
	got := buildCliAuthorizeURL("https://example.test", "Claude", "CHAL", "http://127.0.0.1:31337/cb", "STATE")
	u, err := url.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	if u.Host != "example.test" {
		t.Errorf("host: %s", u.Host)
	}
	if u.Path != "/cli_authorization/new" {
		t.Errorf("path: %s", u.Path)
	}
	q := u.Query()
	if q.Get("name") != "Claude" {
		t.Errorf("name: %q", q.Get("name"))
	}
	if q.Get("code_challenge") != "CHAL" {
		t.Errorf("code_challenge: %q", q.Get("code_challenge"))
	}
	if q.Get("code_challenge_method") != "S256" {
		t.Errorf("code_challenge_method: %q", q.Get("code_challenge_method"))
	}
	if q.Get("redirect_uri") != "http://127.0.0.1:31337/cb" {
		t.Errorf("redirect_uri: %q", q.Get("redirect_uri"))
	}
	if q.Get("state") != "STATE" {
		t.Errorf("state: %q", q.Get("state"))
	}
	if q.Get("scope") != "read reply" {
		t.Errorf("scope: %q", q.Get("scope"))
	}
}

func TestExchangeCodeForToken_Happy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/token" {
			t.Errorf("path: %s", r.URL.Path)
		}
		r.ParseForm()
		if r.PostForm.Get("grant_type") != "authorization_code" {
			t.Errorf("grant_type: %s", r.PostForm.Get("grant_type"))
		}
		if r.PostForm.Get("code") != "the_code" {
			t.Errorf("code: %s", r.PostForm.Get("code"))
		}
		if r.PostForm.Get("code_verifier") != "the_verifier" {
			t.Errorf("verifier: %s", r.PostForm.Get("code_verifier"))
		}
		if r.PostForm.Get("client_id") != "client_xyz" {
			t.Errorf("client_id: %s", r.PostForm.Get("client_id"))
		}
		w.Write([]byte(`{"access_token":"tok_long_lived"}`))
	}))
	defer srv.Close()

	tok, err := exchangeCodeForToken(srv.URL, "the_code", "the_verifier", "http://127.0.0.1:1234/cb", "client_xyz")
	if err != nil {
		t.Fatal(err)
	}
	if tok != "tok_long_lived" {
		t.Errorf("got %q", tok)
	}
}

func TestFetchToken_UsesAccessTokenWhenSet(t *testing.T) {
	// If AccessToken is in the config, fetchToken returns it directly
	// without hitting /oauth/token.
	tok, err := fetchToken(&config{APIURL: "http://unused", AccessToken: "stored_tok"})
	if err != nil {
		t.Fatal(err)
	}
	if tok != "stored_tok" {
		t.Errorf("got %q", tok)
	}
}

func TestExchangeCodeForToken_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"invalid_grant"}`))
	}))
	defer srv.Close()

	_, err := exchangeCodeForToken(srv.URL, "code", "verifier", "http://127.0.0.1:1/cb", "client")
	if err == nil {
		t.Fatal("want error")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("status code missing: %v", err)
	}
}

func TestExchangeCodeForToken_EmptyAccessTokenIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"token_type":"Bearer"}`))
	}))
	defer srv.Close()

	_, err := exchangeCodeForToken(srv.URL, "code", "verifier", "http://127.0.0.1:1/cb", "client")
	if err == nil {
		t.Fatal("want error for empty access_token")
	}
}

func TestAuthErrorMessages(t *testing.T) {
	cases := []struct {
		code, desc        string
		wantBrowserSubstr string
		wantCLISubstr     string
	}{
		{"access_denied", "", "cancelled", "cancelled"},
		{"server_error", "", "Authorization failed", "rejected: server_error"},
		{"server_error", "Database is on fire", "Database is on fire", "rejected: server_error"},
	}
	for _, c := range cases {
		browser, cli := authErrorMessages(c.code, c.desc)
		if !strings.Contains(browser, c.wantBrowserSubstr) {
			t.Errorf("browser msg for code=%q desc=%q: %q lacks %q", c.code, c.desc, browser, c.wantBrowserSubstr)
		}
		if !strings.Contains(cli, c.wantCLISubstr) {
			t.Errorf("cli msg for code=%q desc=%q: %q lacks %q", c.code, c.desc, cli, c.wantCLISubstr)
		}
	}
}

func TestPrintJSON_PrettyPrintsValidJSON(t *testing.T) {
	var out bytes.Buffer
	printJSON(&out, []byte(`{"id":1,"name":"x"}`))
	got := out.String()
	if !strings.Contains(got, "\"id\": 1") {
		t.Errorf("expected indented JSON, got %q", got)
	}
	if !strings.Contains(got, "\n") {
		t.Errorf("expected newlines from indent, got %q", got)
	}
}

func TestPrintJSON_FallsBackOnInvalidJSON(t *testing.T) {
	var out bytes.Buffer
	printJSON(&out, []byte("not json at all"))
	if !strings.Contains(out.String(), "not json at all") {
		t.Errorf("expected raw passthrough, got %q", out.String())
	}
}

func TestRun_AuthStatus_NotAuthenticated(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SUPPYHQ_CLIENT_ID", "")
	t.Setenv("SUPPYHQ_CLIENT_SECRET", "")
	t.Setenv("SUPPYHQ_NO_VERSION_CHECK", "1")

	var stdout bytes.Buffer
	code := run([]string{"auth", "status"}, nil, &stdout, io.Discard)
	if code != 0 {
		t.Errorf("got %d", code)
	}
	if !strings.Contains(stdout.String(), "Not authenticated") {
		t.Errorf("expected 'Not authenticated', got %q", stdout.String())
	}
}

func TestRun_AuthStatus_WithAccessToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// We never call the OAuth token endpoint when AccessToken is set.
		t.Errorf("token endpoint should not be hit when AccessToken is in config (%s)", r.URL.Path)
	}))
	defer srv.Close()

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SUPPYHQ_API_URL", "")
	t.Setenv("SUPPYHQ_CLIENT_ID", "")
	t.Setenv("SUPPYHQ_CLIENT_SECRET", "")
	t.Setenv("SUPPYHQ_NO_VERSION_CHECK", "1")

	saveConfig(&config{
		APIURL:      srv.URL,
		ClientID:    "agent_uid",
		AccessToken: "the_token",
		AgentName:   "Claude on my MacBook",
	})

	var stdout bytes.Buffer
	code := run([]string{"auth", "status"}, nil, &stdout, io.Discard)
	if code != 0 {
		t.Fatalf("got %d", code)
	}
	if !strings.Contains(stdout.String(), `"Claude on my MacBook"`) {
		t.Errorf("expected agent name in status, got %q", stdout.String())
	}
}

func TestRun_AuthLogout_RemovesConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SUPPYHQ_NO_VERSION_CHECK", "1")
	saveConfig(&config{APIURL: "http://x", ClientID: "id", ClientSecret: "secret"})

	cfgPath := filepath.Join(home, configRel)
	if _, err := os.Stat(cfgPath); err != nil {
		t.Fatalf("setup: config not written: %v", err)
	}

	var stdout bytes.Buffer
	code := run([]string{"auth", "logout"}, nil, &stdout, io.Discard)
	if code != 0 {
		t.Fatalf("got %d", code)
	}
	if _, err := os.Stat(cfgPath); !os.IsNotExist(err) {
		t.Errorf("config still on disk after logout: err=%v", err)
	}
	if !strings.Contains(stdout.String(), "Logged out") {
		t.Errorf("expected 'Logged out', got %q", stdout.String())
	}
}

func TestRun_AuthLogout_NoOpWhenNotAuthenticated(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SUPPYHQ_NO_VERSION_CHECK", "1")

	var stdout bytes.Buffer
	code := run([]string{"auth", "logout"}, nil, &stdout, io.Discard)
	if code != 0 {
		t.Fatalf("got %d", code)
	}
	if !strings.Contains(stdout.String(), "Already logged out") {
		t.Errorf("expected 'Already logged out', got %q", stdout.String())
	}
}

func TestApiGET_Forbidden(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"error":"insufficient_scope"}`))
	}))
	defer srv.Close()

	_, err := apiGET(&config{APIURL: srv.URL}, "tok", "/api/v1/x")
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Errorf("expected 403 in error, got %v", err)
	}
}

func TestApiPOST_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("boom"))
	}))
	defer srv.Close()

	_, err := apiPOST(&config{APIURL: srv.URL}, "tok", "/x", url.Values{"k": {"v"}})
	if err == nil || !strings.Contains(err.Error(), "500") {
		t.Errorf("expected 500 in error, got %v", err)
	}
}

func TestSaveConfig_HasOmittedFieldsInJSON(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SUPPYHQ_API_URL", "")
	t.Setenv("SUPPYHQ_CLIENT_ID", "")
	t.Setenv("SUPPYHQ_CLIENT_SECRET", "")

	// Browser flow: only api_url, client_id, access_token, agent_name set.
	if err := saveConfig(&config{
		APIURL:      "https://example.test",
		ClientID:    "id_uid",
		AccessToken: "t_long",
		AgentName:   "Claude",
	}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(home, configRel))
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	// client_secret was empty + omitempty → must not appear.
	if strings.Contains(body, "client_secret") {
		t.Errorf("client_secret should be omitted when empty: %s", body)
	}
	// access_token must be present.
	if !strings.Contains(body, "access_token") {
		t.Errorf("access_token must persist: %s", body)
	}
}

func TestSplitDraftFlag(t *testing.T) {
	cases := []struct {
		in        []string
		wantRest  []string
		wantDraft bool
	}{
		{[]string{"42"}, []string{"42"}, false},
		{[]string{"42", "--draft"}, []string{"42"}, true},
		{[]string{"--draft", "42"}, []string{"42"}, true},
		{[]string{"42", "<body>", "-d"}, []string{"42", "<body>"}, true},
		{[]string{"42", "<body>"}, []string{"42", "<body>"}, false},
	}
	for _, c := range cases {
		got, draft := splitDraftFlag(c.in)
		if draft != c.wantDraft {
			t.Errorf("draft for %v: got %v, want %v", c.in, draft, c.wantDraft)
		}
		if len(got) != len(c.wantRest) {
			t.Errorf("rest for %v: got %v, want %v", c.in, got, c.wantRest)
			continue
		}
		for i := range got {
			if got[i] != c.wantRest[i] {
				t.Errorf("rest[%d] for %v: got %q, want %q", i, c.in, got[i], c.wantRest[i])
			}
		}
	}
}

func TestRun_ReplyDraftSetsParam(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth/token":
			w.Write([]byte(`{"access_token":"tok"}`))
		case "/api/v1/conversations/42/messages":
			r.ParseForm()
			if r.PostForm.Get("draft") != "true" {
				t.Errorf("draft param: want 'true', got %q", r.PostForm.Get("draft"))
			}
			if r.PostForm.Get("body_html") != "<p>draft</p>" {
				t.Errorf("body_html: %v", r.PostForm)
			}
			w.Write([]byte(`{"id":99,"is_draft":true}`))
		}
	}))
	defer srv.Close()

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SUPPYHQ_API_URL", "")
	t.Setenv("SUPPYHQ_CLIENT_ID", "")
	t.Setenv("SUPPYHQ_CLIENT_SECRET", "")
	saveConfig(&config{APIURL: srv.URL, ClientID: "id", ClientSecret: "secret"})

	var stdout, stderr bytes.Buffer
	code := run([]string{"reply", "42", "--draft"}, strings.NewReader("<p>draft</p>"), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("got %d (stderr=%s)", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"is_draft"`) {
		t.Errorf("missing is_draft echo: %s", stdout.String())
	}
}

func TestVerifyChecksum_Match(t *testing.T) {
	tmp := t.TempDir()
	archive := filepath.Join(tmp, "suppyhq_0.1.0_darwin_arm64.tar.gz")
	if err := os.WriteFile(archive, []byte("hello world"), 0o644); err != nil {
		t.Fatal(err)
	}
	// sha256 of "hello world"
	expected := "b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(expected + "  suppyhq_0.1.0_darwin_arm64.tar.gz\n"))
	}))
	defer srv.Close()

	if err := verifyChecksum(archive, srv.URL, "suppyhq_0.1.0_darwin_arm64.tar.gz"); err != nil {
		t.Fatalf("expected ok, got %v", err)
	}
}

func TestVerifyChecksum_Mismatch(t *testing.T) {
	tmp := t.TempDir()
	archive := filepath.Join(tmp, "x.tar.gz")
	if err := os.WriteFile(archive, []byte("hello world"), 0o644); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("0000000000000000000000000000000000000000000000000000000000000000  x.tar.gz\n"))
	}))
	defer srv.Close()

	if err := verifyChecksum(archive, srv.URL, "x.tar.gz"); err == nil {
		t.Fatal("want mismatch error")
	}
}

func TestVerifyChecksum_FileNotInChecksumsList(t *testing.T) {
	tmp := t.TempDir()
	archive := filepath.Join(tmp, "y.tar.gz")
	if err := os.WriteFile(archive, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("aaaa  other.tar.gz\n"))
	}))
	defer srv.Close()

	err := verifyChecksum(archive, srv.URL, "y.tar.gz")
	if err == nil || !strings.Contains(err.Error(), "no checksum entry") {
		t.Errorf("want 'no checksum entry', got %v", err)
	}
}

func TestLatestReleaseTag(t *testing.T) {
	// Stub out the github call by spinning a server, but latestReleaseTag
	// hits the real api host. Skip — covered by the integration smoke test
	// which actually runs against GitHub. Keep this test as a no-op so the
	// suite documents the surface.
	t.Skip("hits live GitHub API; covered by manual smoke test")
}

func TestRun_ReplyEndToEnd(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth/token":
			w.Write([]byte(`{"access_token":"tok"}`))
		case "/api/v1/conversations/42/messages":
			r.ParseForm()
			if r.PostForm.Get("body_html") != "<p>from stdin</p>" {
				t.Errorf("body_html: %v", r.PostForm)
			}
			w.Write([]byte(`{"id":99,"send_at":"2026-01-01T00:00:30Z"}`))
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SUPPYHQ_API_URL", "")
	t.Setenv("SUPPYHQ_CLIENT_ID", "")
	t.Setenv("SUPPYHQ_CLIENT_SECRET", "")
	if err := saveConfig(&config{APIURL: srv.URL, ClientID: "id", ClientSecret: "secret"}); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := run([]string{"reply", "42"}, strings.NewReader("<p>from stdin</p>"), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("want 0, got %d (stderr=%s)", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"id"`) {
		t.Errorf("missing id in output: %s", stdout.String())
	}
}
