package main

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const getRetrySchedule = 3 // 1s, 2s, 4s

func apiGET(cfg *config, token, path string) ([]byte, error) {
	return apiRequest(cfg, token, "GET", path, nil)
}

func apiPOST(cfg *config, token, path string, form url.Values) ([]byte, error) {
	return apiRequest(cfg, token, "POST", path, form)
}

func apiRequest(cfg *config, token, method, path string, form url.Values) ([]byte, error) {
	isWrite := method != "GET" && method != "HEAD"
	var lastErr error

	attempts := 1
	if !isWrite {
		attempts = 1 + getRetrySchedule
	}

	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 {
			delay := time.Duration(1<<uint(attempt-1)) * time.Second
			time.Sleep(delay)
		}

		body, status, err := apiRequestOnce(cfg, token, method, path, form)
		if err != nil {
			lastErr = errNetwork(err.Error())
			if !isWrite && attempt+1 < attempts {
				continue
			}
			return nil, lastErr
		}

		if status >= 200 && status < 300 {
			return body, nil
		}

		lastErr = classifyHTTPError(status, string(body), isWrite)
		if isWrite || !shouldRetryGET(status, attempt, attempts) {
			return nil, lastErr
		}
	}

	return nil, lastErr
}

func shouldRetryGET(status, attempt, attempts int) bool {
	if attempt+1 >= attempts {
		return false
	}
	return status == 429 || status >= 500
}

func classifyHTTPError(status int, body string, isWrite bool) error {
	body = strings.TrimSpace(body)
	switch status {
	case 401:
		return errAuth("Unauthorized", "Run: suppyhq auth login")
	case 403:
		return errForbidden("Forbidden", "Grant the required scope at https://app.suppyhq.com/agents")
	case 429:
		hint := "Back off using the 1s / 2s / 4s schedule, then hold 60s if still limited."
		if isWrite {
			hint = "This was a write. Do not retry — check the thread before sending again."
			return errRateLimit("Too many requests", hint, false)
		}
		return errRateLimit("Too many requests", hint, true)
	default:
		return errAPI(status, body)
	}
}

func apiRequestOnce(cfg *config, token, method, path string, form url.Values) ([]byte, int, error) {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequest(method, strings.TrimRight(cfg.APIURL, "/")+path, body)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	return respBody, resp.StatusCode, nil
}

func inboxBreadcrumbs(data any) []breadcrumb {
	crumbs := []breadcrumb{}
	// Suggest thread reads for the first few open conversations when possible.
	arr, ok := data.([]any)
	if !ok {
		return crumbs
	}
	for i, item := range arr {
		if i >= 3 {
			break
		}
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		id := fmt.Sprintf("%v", m["id"])
		if id == "" || id == "<nil>" {
			continue
		}
		crumbs = append(crumbs, breadcrumb{Action: "read", Cmd: "suppyhq thread " + id})
	}
	return crumbs
}

func threadBreadcrumbs(id string, draft bool) []breadcrumb {
	crumbs := []breadcrumb{
		{Action: "list", Cmd: "suppyhq inbox"},
	}
	if draft {
		crumbs = append(crumbs, breadcrumb{Action: "send", Cmd: "suppyhq reply " + id})
	} else {
		crumbs = append(crumbs, breadcrumb{Action: "draft", Cmd: "suppyhq reply " + id + " --draft"})
	}
	return crumbs
}

func summarizeInbox(data any) string {
	arr, ok := data.([]any)
	if !ok {
		return "conversations"
	}
	return fmt.Sprintf("%d conversations", len(arr))
}

func summarizeThread(data any) string {
	m, ok := data.(map[string]any)
	if !ok {
		return "conversation"
	}
	subject, _ := m["subject"].(string)
	if subject != "" {
		return subject
	}
	return "conversation"
}
