package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// A SuppyHQ API that enforces permissions the way the real one does:
// the token endpoint refuses any scope the agent doesn't hold, drafting
// needs `draft` (or the pre-split `reply`), and sending needs `send` (or
// `reply`). The Rails app has its own integration test for this rule; this
// one proves the CLI behaves correctly on both sides of it.
type fakeAgentAPI struct {
	held map[string]bool

	mu     sync.Mutex
	sends  int // send attempts that reached the messages endpoint
	sent   int // sends the API accepted
	drafts int // drafts the API accepted
}

func newFakeAgentAPI(t *testing.T, agentScopes string) (*fakeAgentAPI, *httptest.Server) {
	t.Helper()
	api := &fakeAgentAPI{held: map[string]bool{}}
	for _, s := range strings.Fields(agentScopes) {
		api.held[s] = true
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/oauth/token":
			_ = r.ParseForm()
			requested := strings.Fields(r.Form.Get("scope"))
			for _, s := range requested {
				if !api.held[s] {
					w.WriteHeader(http.StatusBadRequest)
					_, _ = w.Write([]byte(`{"error":"invalid_scope"}`))
					return
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"access_token": strings.Join(requested, "+")})

		case strings.HasSuffix(r.URL.Path, "/messages") && r.Method == http.MethodPost:
			granted := map[string]bool{}
			for _, s := range strings.Split(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), "+") {
				granted[s] = true
			}
			draft := readDraftFlag(r)

			api.mu.Lock()
			defer api.mu.Unlock()
			if !draft {
				api.sends++
			}
			allowed := (draft && (granted["draft"] || granted["reply"])) || (!draft && (granted["send"] || granted["reply"]))
			if !allowed {
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"error":"insufficient_scope"}`))
				return
			}
			if draft {
				api.drafts++
			} else {
				api.sent++
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":1,"is_draft":` + map[bool]string{true: "true", false: "false"}[draft] + `,"send_at":null,"sent_at":null}`))

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return api, srv
}

// The CLI may send the flag as form data or JSON. Read either.
func readDraftFlag(r *http.Request) bool {
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		switch v := body["draft"].(type) {
		case bool:
			return v
		case string:
			return v == "true" || v == "1"
		}
		return false
	}
	_ = r.ParseForm()
	v := r.Form.Get("draft")
	return v == "true" || v == "1"
}

// Runs the real CLI entry point as an agent with the given credentials.
func runAgent(t *testing.T, apiURL string, args ...string) (code int, out map[string]any) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SUPPYHQ_NO_VERSION_CHECK", "1")
	t.Setenv("SUPPYHQ_API_URL", apiURL)
	t.Setenv("SUPPYHQ_CLIENT_ID", "agent")
	t.Setenv("SUPPYHQ_CLIENT_SECRET", "secret")

	var stdout, stderr bytes.Buffer
	code = run(append(args, "--json"), strings.NewReader("<p>Refunded.</p>"), &stdout, &stderr)
	raw := stdout.String() + stderr.String()
	if err := json.Unmarshal([]byte(strings.TrimSpace(firstJSONObject(raw))), &out); err != nil {
		t.Fatalf("CLI output is not JSON: %v\n%s", err, raw)
	}
	return code, out
}

func firstJSONObject(s string) string {
	if i := strings.Index(s, "{"); i >= 0 {
		return s[i:]
	}
	return s
}

func TestAgentPermissions(t *testing.T) {
	t.Run("an agent with draft but not send can draft", func(t *testing.T) {
		api, srv := newFakeAgentAPI(t, "read draft")

		code, out := runAgent(t, srv.URL, "reply", "1", "--draft")

		if code != 0 || out["ok"] != true {
			t.Fatalf("draft should succeed, got exit %d: %v", code, out)
		}
		if api.drafts != 1 {
			t.Errorf("API accepted %d drafts, want 1", api.drafts)
		}
	})

	t.Run("an agent with draft but not send cannot send, and is told to draft instead", func(t *testing.T) {
		api, srv := newFakeAgentAPI(t, "read draft")

		code, out := runAgent(t, srv.URL, "reply", "1", "--yes")

		if code != exitForbidden {
			t.Errorf("exit code %d, want %d (forbidden)", code, exitForbidden)
		}
		if out["ok"] != false || out["code"] != "forbidden" {
			t.Errorf("want a forbidden error, got %v", out)
		}
		hint, _ := out["hint"].(string)
		for _, want := range []string{"--draft", "--allow-send"} {
			if !strings.Contains(hint, want) {
				t.Errorf("hint should tell the agent about %s, got %q", want, hint)
			}
		}
		if api.sent != 0 {
			t.Errorf("API accepted %d sends from an agent without send permission", api.sent)
		}
		if api.sends != 1 {
			t.Errorf("CLI tried to send %d times, want exactly 1: a refused write must not be retried", api.sends)
		}
	})

	t.Run("an agent with send can send", func(t *testing.T) {
		api, srv := newFakeAgentAPI(t, "read draft send")

		code, out := runAgent(t, srv.URL, "reply", "1", "--yes")

		if code != 0 || out["ok"] != true {
			t.Fatalf("send should succeed, got exit %d: %v", code, out)
		}
		if api.sent != 1 {
			t.Errorf("API accepted %d sends, want 1", api.sent)
		}
	})

	t.Run("an agent connected before the split can still draft and send", func(t *testing.T) {
		api, srv := newFakeAgentAPI(t, "read reply")

		draftCode, _ := runAgent(t, srv.URL, "reply", "1", "--draft")
		sendCode, _ := runAgent(t, srv.URL, "reply", "1", "--yes")

		if draftCode != 0 || sendCode != 0 {
			t.Fatalf("legacy agent: draft exit %d, send exit %d, want 0 and 0", draftCode, sendCode)
		}
		if api.drafts != 1 || api.sent != 1 {
			t.Errorf("API accepted %d drafts and %d sends, want 1 and 1", api.drafts, api.sent)
		}
	})

	t.Run("a read-only agent cannot draft", func(t *testing.T) {
		api, srv := newFakeAgentAPI(t, "read")

		code, out := runAgent(t, srv.URL, "reply", "1", "--draft")

		if code != exitForbidden || out["code"] != "forbidden" {
			t.Errorf("want forbidden, got exit %d: %v", code, out)
		}
		if api.drafts != 0 {
			t.Errorf("API accepted %d drafts from a read-only agent", api.drafts)
		}
	})
}
