package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

// Typed exit codes — agents branch on these without parsing stderr.
const (
	exitOK        = 0
	exitUsage     = 1
	exitNotFound  = 2
	exitAuth      = 3
	exitForbidden = 4
	exitRateLimit = 5
	exitNetwork   = 6
	exitAPI       = 7
	exitAmbiguous = 8
)

type outputMode int

const (
	modeAuto outputMode = iota
	modeHuman
	modeJSON
	modeQuiet
	modeAgent
)

type formatFlags struct {
	JSON   bool
	Quiet  bool
	Agent  bool
	Styled bool
}

func parseFormatFlags(args []string) (formatFlags, []string) {
	var ff formatFlags
	rest := make([]string, 0, len(args))
	for _, a := range args {
		switch a {
		case "--json", "-j":
			ff.JSON = true
		case "--quiet", "-q":
			ff.Quiet = true
		case "--agent":
			ff.Agent = true
		case "--styled":
			ff.Styled = true
		default:
			rest = append(rest, a)
		}
	}
	return ff, rest
}

func (ff formatFlags) mode() outputMode {
	switch {
	case ff.Agent:
		return modeAgent
	case ff.Quiet:
		return modeQuiet
	case ff.JSON:
		return modeJSON
	case ff.Styled:
		return modeHuman
	default:
		return modeAuto
	}
}

func isTTY(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return (info.Mode() & os.ModeCharDevice) != 0
}

func machineOutput(mode outputMode, stdout io.Writer) bool {
	switch mode {
	case modeAgent, modeQuiet, modeJSON:
		return true
	case modeHuman:
		return false
	default:
		return !isTTY(stdout)
	}
}

type breadcrumb struct {
	Action string `json:"action"`
	Cmd    string `json:"cmd"`
}

type cliError struct {
	Message   string
	Code      string
	Retryable bool
	Hint      string
	Exit      int
}

func (e *cliError) Error() string { return e.Message }

func errUsage(msg string) *cliError {
	return &cliError{Message: msg, Code: "usage_error", Exit: exitUsage, Hint: "Run: suppyhq help"}
}

func errAuth(msg, hint string) *cliError {
	return &cliError{Message: msg, Code: "auth_error", Exit: exitAuth, Hint: hint}
}

func errForbidden(msg, hint string) *cliError {
	return &cliError{Message: msg, Code: "forbidden", Exit: exitForbidden, Hint: hint}
}

func errRateLimit(msg, hint string, retryable bool) *cliError {
	return &cliError{Message: msg, Code: "rate_limit", Exit: exitRateLimit, Hint: hint, Retryable: retryable}
}

func errNetwork(msg string) *cliError {
	return &cliError{Message: msg, Code: "network_error", Exit: exitNetwork, Retryable: true, Hint: "Check your connection and retry."}
}

func errAPI(status int, body string) *cliError {
	retryable := status >= 500
	code := "api_error"
	exit := exitAPI
	if status == 404 {
		code = "not_found"
		exit = exitNotFound
	}
	return &cliError{
		Message:   fmt.Sprintf("HTTP %d: %s", status, body),
		Code:      code,
		Exit:      exit,
		Retryable: retryable,
		Hint:      apiErrorHint(status),
	}
}

func apiErrorHint(status int) string {
	switch status {
	case 401:
		return "Run: suppyhq auth login"
	case 403:
		return "Grant the required scope at https://app.suppyhq.com/agents"
	case 404:
		return "Check the conversation or customer id."
	case 429:
		return "Rate limited. Back off before retrying."
	default:
		return ""
	}
}

type successEnvelope struct {
	OK          bool         `json:"ok"`
	Data        any          `json:"data"`
	Summary     string       `json:"summary,omitempty"`
	Breadcrumbs []breadcrumb `json:"breadcrumbs,omitempty"`
}

type errorEnvelope struct {
	OK        bool   `json:"ok"`
	Error     string `json:"error"`
	Code      string `json:"code"`
	Retryable bool   `json:"retryable"`
	Hint      string `json:"hint,omitempty"`
}

func emitSuccess(stdout io.Writer, mode outputMode, data any, summary string, crumbs []breadcrumb) int {
	if mode == modeAgent || mode == modeQuiet {
		return writeRawJSON(stdout, data)
	}
	if mode == modeHuman || (mode == modeAuto && isTTY(stdout)) {
		if summary != "" {
			fmt.Fprintln(stdout, summary)
		}
		return writeRawJSON(stdout, data)
	}
	env := successEnvelope{OK: true, Data: data, Summary: summary, Breadcrumbs: crumbs}
	return writeJSON(stdout, env)
}

func emitError(stderr, stdout io.Writer, mode outputMode, err error) int {
	ce := asCLIError(err)
	env := errorEnvelope{
		OK:        false,
		Error:     ce.Message,
		Code:      ce.Code,
		Retryable: ce.Retryable,
		Hint:      ce.Hint,
	}
	if machineOutput(mode, stdout) {
		writeJSON(stdout, env)
	} else {
		fmt.Fprintf(stderr, "suppyhq: %s\n", ce.Message)
		if ce.Hint != "" {
			fmt.Fprintf(stderr, "hint: %s\n", ce.Hint)
		}
	}
	return ce.Exit
}

func asCLIError(err error) *cliError {
	if err == nil {
		return &cliError{Message: "unknown error", Code: "api_error", Exit: exitAPI}
	}
	if ce, ok := err.(*cliError); ok {
		return ce
	}
	return &cliError{Message: err.Error(), Code: "api_error", Exit: exitAPI}
}

func writeJSON(w io.Writer, v any) int {
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		fmt.Fprintf(w, `{"ok":false,"error":"encode failed","code":"api_error","retryable":false}`)
		return exitAPI
	}
	fmt.Fprintln(w, string(out))
	return exitOK
}

func writeRawJSON(w io.Writer, data any) int {
	var raw any
	switch v := data.(type) {
	case []byte:
		if err := json.Unmarshal(v, &raw); err != nil {
			fmt.Fprintln(w, string(v))
			return exitOK
		}
	case json.RawMessage:
		if err := json.Unmarshal(v, &raw); err != nil {
			fmt.Fprintln(w, string(v))
			return exitOK
		}
	default:
		raw = data
	}
	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return writeJSON(w, map[string]string{"error": "encode failed"})
	}
	fmt.Fprintln(w, string(out))
	return exitOK
}

func decodeJSONData(raw []byte) (any, error) {
	var data any
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	if err := dec.Decode(&data); err != nil {
		return nil, err
	}
	return data, nil
}
