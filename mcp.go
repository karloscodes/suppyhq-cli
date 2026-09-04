package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const mcpDescribeAction = "describe"

type mcpDomain struct {
	name      string
	toolName  string
	title     string
	readOnly  bool
	actions   map[string]mcpAction
}

type mcpAction struct {
	name     string
	readOnly bool
	schema   map[string]any
}

func allMCPDomains() []mcpDomain {
	return []mcpDomain{
		{
			name: "conversations", toolName: "suppyhq_conversations", title: "SuppyHQ Conversations",
			actions: map[string]mcpAction{
				"list":  {name: "list", readOnly: true, schema: map[string]any{"type": "object", "properties": map[string]any{}}},
				"show":  {name: "show", readOnly: true, schema: map[string]any{"type": "object", "properties": map[string]any{"id": map[string]any{"type": "string", "description": "Conversation id"}}, "required": []string{"id"}}},
				"reply": {name: "reply", readOnly: false, schema: map[string]any{"type": "object", "properties": map[string]any{
					"id": map[string]any{"type": "string"}, "body_html": map[string]any{"type": "string"},
					"draft": map[string]any{"type": "boolean"}, "confirm": map[string]any{"type": "boolean", "description": "Required true to send (not draft)"},
				}, "required": []string{"id", "body_html"}}},
			},
		},
		{
			name: "customers", toolName: "suppyhq_customers", title: "SuppyHQ Customers",
			actions: map[string]mcpAction{
				"list": {name: "list", readOnly: true, schema: map[string]any{"type": "object", "properties": map[string]any{}}},
			},
		},
		{
			name: "identity", toolName: "suppyhq_identity", title: "SuppyHQ Identity",
			actions: map[string]mcpAction{
				"status": {name: "status", readOnly: true, schema: map[string]any{"type": "object", "properties": map[string]any{}}},
			},
		},
	}
}

func runMCP(args []string, stderr io.Writer) int {
	readOnly := false
	var domainFilter []string
	for _, a := range args {
		switch {
		case a == "--read-only":
			readOnly = true
		case strings.HasPrefix(a, "--domains="):
			domainFilter = strings.Split(strings.TrimPrefix(a, "--domains="), ",")
		}
	}

	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintf(stderr, "suppyhq mcp: config: %v\n", err)
		return exitAuth
	}
	if cfg.AccessToken == "" && (cfg.ClientID == "" || cfg.ClientSecret == "") {
		fmt.Fprintln(stderr, "suppyhq mcp: not authenticated. Run: suppyhq auth login")
		return exitAuth
	}

	domains, err := filterMCPDomains(allMCPDomains(), domainFilter, readOnly)
	if err != nil {
		fmt.Fprintf(stderr, "suppyhq mcp: %v\n", err)
		return exitUsage
	}

	logger := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	server := mcp.NewServer(&mcp.Implementation{Name: "suppyhq-cli", Version: Version}, &mcp.ServerOptions{Logger: logger})

	for _, d := range domains {
		domain := d
		tool := &mcp.Tool{
			Name:        domain.toolName,
			Description: domain.description(),
			InputSchema: domain.inputSchema(),
		}
		server.AddTool(tool, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return dispatchMCP(ctx, cfg, domain, req)
		})
	}

	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		fmt.Fprintf(stderr, "suppyhq mcp: %v\n", err)
		return exitAPI
	}
	return exitOK
}

func filterMCPDomains(all []mcpDomain, names []string, readOnly bool) ([]mcpDomain, error) {
	byName := map[string]mcpDomain{}
	for _, d := range all {
		byName[d.name] = d
	}
	pick := names
	if len(pick) == 0 {
		for _, d := range all {
			pick = append(pick, d.name)
		}
	}
	var out []mcpDomain
	for _, name := range pick {
		name = strings.TrimSpace(name)
		d, ok := byName[name]
		if !ok {
			known := make([]string, 0, len(byName))
			for k := range byName {
				known = append(known, k)
			}
			return nil, fmt.Errorf("unknown domain %q (known: %s)", name, strings.Join(known, ", "))
		}
		if readOnly {
			d = d.filterReadOnly()
		}
		out = append(out, d)
	}
	return out, nil
}

func (d mcpDomain) filterReadOnly() mcpDomain {
	actions := map[string]mcpAction{}
	for k, a := range d.actions {
		if a.readOnly {
			actions[k] = a
		}
	}
	d.actions = actions
	return d
}

func (d mcpDomain) description() string {
	names := d.actionNames()
	return fmt.Sprintf("SuppyHQ %s. Actions: %s, describe. Call describe for parameter schemas.", d.name, strings.Join(names, ", "))
}

func (d mcpDomain) actionNames() []string {
	var names []string
	for k := range d.actions {
		names = append(names, k)
	}
	sortStrings(names)
	return names
}

func (d mcpDomain) inputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{"type": "string", "description": "Action name, or describe"},
			"params": map[string]any{"type": "object", "description": "Action parameters"},
		},
		"required": []string{"action"},
	}
}

func dispatchMCP(ctx context.Context, cfg *config, d mcpDomain, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	_ = ctx
	var args struct {
		Action string         `json:"action"`
		Params map[string]any `json:"params"`
	}
	if err := remarshalMCP(req.Params.Arguments, &args); err != nil {
		return mcpError("invalid arguments: %v", err), nil
	}
	if args.Action == mcpDescribeAction {
		target, _ := args.Params["action"].(string)
		return mcpJSON(d.describePayload(target))
	}
	action, ok := d.actions[args.Action]
	if !ok {
		return mcpError("unknown action %q (actions: %s, describe)", args.Action, strings.Join(append(d.actionNames(), mcpDescribeAction), ", ")), nil
	}
	return executeMCPAction(cfg, d, action, args.Params)
}

func (d mcpDomain) describePayload(action string) any {
	if action == "" {
		actions := []map[string]any{}
		for _, name := range d.actionNames() {
			a := d.actions[name]
			actions = append(actions, map[string]any{"action": name, "read_only": a.readOnly, "params_schema": a.schema})
		}
		return map[string]any{"domain": d.name, "tool": d.toolName, "actions": actions}
	}
	a, ok := d.actions[action]
	if !ok {
		return map[string]any{"error": "unknown action"}
	}
	return map[string]any{"action": action, "read_only": a.readOnly, "params_schema": a.schema}
}

func executeMCPAction(cfg *config, d mcpDomain, action mcpAction, params map[string]any) (*mcp.CallToolResult, error) {
	token, err := fetchToken(cfg)
	if err != nil {
		return mcpError("auth: %v", err), nil
	}

	switch d.name + "." + action.name {
	case "conversations.list":
		body, err := apiGET(cfg, token, "/api/v1/conversations")
		if err != nil {
			return mcpError("%v", err), nil
		}
		return mcpRawJSON(body)
	case "conversations.show":
		id := paramString(params, "id")
		if id == "" {
			return mcpError("params.id is required"), nil
		}
		body, err := apiGET(cfg, token, "/api/v1/conversations/"+id)
		if err != nil {
			return mcpError("%v", err), nil
		}
		return mcpRawJSON(body)
	case "conversations.reply":
		id := paramString(params, "id")
		html := paramString(params, "body_html")
		if id == "" || html == "" {
			return mcpError("params.id and params.body_html are required"), nil
		}
		draft := paramBool(params, "draft")
		confirm := paramBool(params, "confirm")
		if !draft && !confirm {
			return mcpError("sending email requires params.confirm=true after operator approval, or params.draft=true"), nil
		}
		form := url.Values{"body_html": {html}}
		if draft {
			form.Set("draft", "true")
		}
		body, err := apiPOST(cfg, token, "/api/v1/conversations/"+id+"/messages", form)
		if err != nil {
			return mcpError("%v", err), nil
		}
		return mcpRawJSON(body)
	case "customers.list":
		body, err := apiGET(cfg, token, "/api/v1/customers")
		if err != nil {
			return mcpError("%v", err), nil
		}
		return mcpRawJSON(body)
	case "identity.status":
		check := checkAuth()
		return mcpJSON(map[string]any{"authenticated": check.Status == "pass", "message": check.Message, "hint": check.Hint})
	default:
		return mcpError("unsupported action"), nil
	}
}

func paramString(params map[string]any, key string) string {
	if params == nil {
		return ""
	}
	v, _ := params[key].(string)
	return strings.TrimSpace(v)
}

func paramBool(params map[string]any, key string) bool {
	if params == nil {
		return false
	}
	v, ok := params[key].(bool)
	return ok && v
}

func mcpError(format string, a ...any) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf(format, a...)}},
	}
}

func mcpJSON(v any) (*mcp.CallToolResult, error) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return mcpError("encode failed"), nil
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(data)}}}, nil
}

func mcpRawJSON(raw []byte) (*mcp.CallToolResult, error) {
	var pretty any
	if err := json.Unmarshal(raw, &pretty); err != nil {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(raw)}}}, nil
	}
	return mcpJSON(pretty)
}

func remarshalMCP(from, to any) error {
	data, err := json.Marshal(from)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, to)
}

func sortStrings(ss []string) {
	for i := 0; i < len(ss); i++ {
		for j := i + 1; j < len(ss); j++ {
			if ss[j] < ss[i] {
				ss[i], ss[j] = ss[j], ss[i]
			}
		}
	}
}
