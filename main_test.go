package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
