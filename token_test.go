package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A stand-in token endpoint that, like Doorkeeper, refuses any scope the
// application doesn't hold.
func tokenServer(t *testing.T, appScopes string, requested *[]string) *httptest.Server {
	t.Helper()
	held := map[string]bool{}
	for _, s := range strings.Fields(appScopes) {
		held[s] = true
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		scope := r.Form.Get("scope")
		*requested = append(*requested, scope)
		for _, s := range strings.Fields(scope) {
			if !held[s] {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_scope"})
				return
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "tok-" + scope})
	}))
}

func TestFetchToken_ClientCredentialsFitsWhateverTheAgentHolds(t *testing.T) {
	cases := []struct {
		name      string
		appScopes string
		want      string
	}{
		{"agent created after the split, with send", "read draft send", "read draft send"},
		{"agent created before the split", "read reply", "read reply"},
		{"agent created after the split, draft only", "read draft", "read draft"},
		{"read-only agent", "read", "read"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var requested []string
			srv := tokenServer(t, tc.appScopes, &requested)
			defer srv.Close()

			token, err := fetchToken(&config{APIURL: srv.URL, ClientID: "id", ClientSecret: "secret"})

			if err != nil {
				t.Fatalf("fetchToken: %v (tried %v)", err, requested)
			}
			if token != "tok-"+tc.want {
				t.Errorf("got token for %q, want %q (tried %v)", strings.TrimPrefix(token, "tok-"), tc.want, requested)
			}
		})
	}
}

// Stepping down is only for invalid_scope. A wrong secret should fail once,
// not be retried with four different scope sets.
func TestFetchToken_DoesNotRetryOtherErrors(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_client"})
	}))
	defer srv.Close()

	_, err := fetchToken(&config{APIURL: srv.URL, ClientID: "id", ClientSecret: "wrong"})

	if err == nil {
		t.Fatal("expected an error for a bad client secret")
	}
	if calls != 1 {
		t.Errorf("token endpoint called %d times, want 1", calls)
	}
}
