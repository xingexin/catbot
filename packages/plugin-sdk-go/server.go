// Package pluginsdk implements catbot plugins over MCP stdio. A plugin owns its
// process and can use any Go library; stdout is reserved for the MCP protocol.
package pluginsdk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

const maxResultBytes = 512 << 10

// Manifest is the public tool subset of plugin.json. Host-only fields such as
// permissions, runtime, entry and task templates remain in the file. ConfigSchema
// contributes format=password annotations to result and error redaction.
type Manifest struct {
	ID           string         `json:"id"`
	Name         string         `json:"name"`
	Version      string         `json:"version"`
	Tools        []Tool         `json:"tools"`
	ConfigSchema map[string]any `json:"configSchema,omitempty"`
}

// Tool declares an MCP tool. Schemas use JSON Schema and input must be an object.
type Tool struct {
	Name         string         `json:"name"`
	Description  string         `json:"description"`
	InputSchema  map[string]any `json:"inputSchema"`
	OutputSchema map[string]any `json:"outputSchema,omitempty"`
}

// ToolContext contains a private configuration copy and host client scoped to
// one tool invocation. OperationID is stable when catbot retries the operation.
type ToolContext struct {
	Config      map[string]any
	Host        *Host
	OperationID string
}

// Handler runs a tool. Honor ctx cancellation and keep diagnostics on stderr.
// Return an object, or a JSON scalar/array which is wrapped in {"value": ...}.
type Handler func(context.Context, map[string]any, ToolContext) (any, error)

// Options configures a server without reading environment variables.
type Options struct {
	Manifest   Manifest
	Config     map[string]any
	HostURL    string
	HostToken  string
	HTTPClient *http.Client
}

// Serve reads plugin.json from the current working directory, loads the catbot
// injected environment, and serves MCP on stdin/stdout until EOF or cancellation.
func Serve(ctx context.Context, handlers map[string]Handler) error {
	data, err := os.ReadFile("plugin.json")
	if err != nil {
		return fmt.Errorf("read plugin manifest: %w", err)
	}
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return errors.New("invalid plugin.json")
	}
	config := map[string]any{}
	if value := os.Getenv("SECRETARY_PLUGIN_CONFIG"); value != "" {
		if err := json.Unmarshal([]byte(value), &config); err != nil || config == nil {
			return errors.New("SECRETARY_PLUGIN_CONFIG must be a JSON object")
		}
	}
	server, err := NewServer(Options{
		Manifest: manifest, Config: config,
		HostURL: os.Getenv("SECRETARY_HOST_URL"), HostToken: os.Getenv("SECRETARY_HOST_TOKEN"),
	}, handlers)
	if err != nil {
		return err
	}
	return server.Run(ctx, &mcp.StdioTransport{})
}

// NewServer validates all tools before exposing them. It can also be connected
// to an MCP in-memory transport in plugin tests. Configuration is copied so tool
// handlers cannot mutate another invocation's config.
func NewServer(options Options, handlers map[string]Handler) (*mcp.Server, error) {
	manifest := options.Manifest
	if manifest.ID == "" || manifest.Version == "" {
		return nil, errors.New("manifest id and version are required")
	}
	config, err := cloneObject(options.Config)
	if err != nil {
		return nil, errors.New("plugin config must contain JSON values")
	}
	configSchema, err := cloneObject(manifest.ConfigSchema)
	if err != nil {
		return nil, errors.New("configSchema must contain JSON values")
	}
	redact := newRedactor(config, configSchema, options.HostToken)
	host, err := NewHost(HostOptions{BaseURL: options.HostURL, Token: options.HostToken, HTTPClient: options.HTTPClient})
	if err != nil {
		return nil, err
	}
	host.redact = redact
	server := mcp.NewServer(&mcp.Implementation{Name: manifest.ID, Version: manifest.Version}, nil)
	seen := map[string]bool{}
	for _, tool := range manifest.Tools {
		if !toolName.MatchString(tool.Name) || seen[tool.Name] {
			return nil, errors.New("tool names must be unique and contain 1–128 letters, digits, _, -, or .")
		}
		seen[tool.Name] = true
		handler := handlers[tool.Name]
		if handler == nil {
			return nil, fmt.Errorf("missing tool handler: %s", tool.Name)
		}
		input, err := cloneObject(tool.InputSchema)
		if err != nil || input["type"] != "object" {
			return nil, fmt.Errorf("tool %s inputSchema must have type object", tool.Name)
		}
		validateInput, err := compileSchema(input)
		if err != nil {
			return nil, fmt.Errorf("invalid inputSchema for %s: %s", tool.Name, redact(err.Error()))
		}
		var output map[string]any
		var validateOutput *jsonschema.Schema
		if tool.OutputSchema != nil {
			output, err = cloneObject(tool.OutputSchema)
			if err != nil {
				return nil, fmt.Errorf("invalid outputSchema for %s", tool.Name)
			}
			validateOutput, err = compileSchema(output)
			if err != nil {
				return nil, fmt.Errorf("invalid outputSchema for %s: %s", tool.Name, redact(err.Error()))
			}
		}
		description := &mcp.Tool{Name: tool.Name, Description: tool.Description, InputSchema: input}
		if output != nil {
			description.OutputSchema = output
		}
		server.AddTool(description, wrapHandler(handler, config, host, validateInput, validateOutput))
	}
	return server, nil
}

var toolName = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)

type noExternalSchemas struct{}

func (noExternalSchemas) Load(string) (any, error) {
	return nil, errors.New("external schema references are not supported")
}
func compileSchema(value map[string]any) (*jsonschema.Schema, error) {
	compiler := jsonschema.NewCompiler()
	compiler.UseLoader(noExternalSchemas{})
	const resource = "https://catbot.invalid/plugin-tool-schema"
	if err := compiler.AddResource(resource, value); err != nil {
		return nil, err
	}
	return compiler.Compile(resource)
}

func wrapHandler(
	handler Handler,
	config map[string]any,
	host *Host,
	input *jsonschema.Schema,
	output *jsonschema.Schema,
) mcp.ToolHandler {
	return func(ctx context.Context, request *mcp.CallToolRequest) (result *mcp.CallToolResult, err error) {
		fail := func(message string) (*mcp.CallToolResult, error) {
			message = host.redact(message)
			if len(message) > maxResultBytes {
				message = "Tool error exceeds 512 KB"
			}
			return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: message}}}, nil
		}
		defer func() {
			if recover() != nil {
				result, err = fail("Tool panicked")
			}
		}()
		if err := ctx.Err(); err != nil {
			return fail(err.Error())
		}
		args := map[string]any{}
		if len(request.Params.Arguments) > 0 {
			if err := json.Unmarshal(request.Params.Arguments, &args); err != nil || args == nil {
				return fail("Invalid arguments: expected a JSON object")
			}
		}
		if err := input.Validate(args); err != nil {
			return fail("Invalid arguments: " + err.Error())
		}
		operationID, _ := request.Params.Meta["secretary/operationId"].(string)
		scoped := *host
		scoped.operationID = operationID
		privateConfig, _ := cloneObject(config)
		value, err := handler(ctx, args, ToolContext{Config: privateConfig, Host: &scoped, OperationID: operationID})
		if err != nil {
			return fail(err.Error())
		}
		if err := ctx.Err(); err != nil {
			return fail(err.Error())
		}
		normalized, err := normalizeResult(value)
		if err != nil {
			return fail("Tool output must contain JSON values")
		}
		if output != nil {
			if err := output.Validate(normalized); err != nil {
				return fail("Invalid tool output")
			}
		}
		data, _ := json.Marshal(normalized)
		if len(data) > maxResultBytes {
			return fail("Tool result exceeds 512 KB; save an artifact and return its ID")
		}
		sanitized := redactJSON(normalized, host.redact)
		data, _ = json.Marshal(sanitized)
		if len(data) > maxResultBytes {
			return fail("Tool result exceeds 512 KB; save an artifact and return its ID")
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(data)}}, StructuredContent: sanitized}, nil
	}
}

func normalizeResult(value any) (map[string]any, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var normalized any
	if err := json.Unmarshal(data, &normalized); err != nil {
		return nil, err
	}
	if object, ok := normalized.(map[string]any); ok && object != nil {
		return object, nil
	}
	return map[string]any{"value": normalized}, nil
}
func cloneObject(value map[string]any) (map[string]any, error) {
	if value == nil {
		return map[string]any{}, nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	result := map[string]any{}
	err = json.Unmarshal(data, &result)
	return result, err
}

// Redact decoded values, rather than JSON text, so quotes and backslashes in
// a secret cannot evade redaction or corrupt the structured result.
func redactJSON(value any, redact func(string) string) any {
	switch value := value.(type) {
	case string:
		return redact(value)
	case map[string]any:
		result := make(map[string]any, len(value))
		for key, item := range value {
			result[redact(key)] = redactJSON(item, redact)
		}
		return result
	case []any:
		result := make([]any, len(value))
		for i, item := range value {
			result[i] = redactJSON(item, redact)
		}
		return result
	default:
		return value
	}
}

func newRedactor(config map[string]any, schema map[string]any, token string) func(string) string {
	secrets := []string{}
	if token != "" {
		secrets = append(secrets, token)
	}
	var collect func(any, bool)
	collect = func(value any, sensitive bool) {
		switch value := value.(type) {
		case map[string]any:
			for key, item := range value {
				collect(item, sensitive || secretKey.MatchString(key))
			}
		case []any:
			for _, item := range value {
				collect(item, sensitive)
			}
		case string:
			if sensitive && value != "" {
				secrets = append(secrets, value)
			}
		}
	}
	collect(config, false)
	collectSchemaSecrets(config, schema, func(value any) { collect(value, true) })
	// Prefer the longest overlapping secret and never re-process replacement text.
	slices.SortFunc(secrets, func(a, b string) int { return len(b) - len(a) })
	replacements := make([]string, 0, len(secrets)*2)
	for _, secret := range secrets {
		replacements = append(replacements, secret, "[REDACTED]")
	}
	return strings.NewReplacer(replacements...).Replace
}

var secretKey = regexp.MustCompile(`(?i)password|secret|token|key`)

// Schema annotations, rather than property names alone, identify passwords such
// as an "auth" field. Compound schemas are conservatively combined: marking a
// field secret in any branch is enough to redact its actual configured value.
func collectSchemaSecrets(value any, schema map[string]any, collect func(any)) {
	if schema["format"] == "password" {
		collect(value)
		return
	}
	for _, keyword := range []string{"allOf", "anyOf", "oneOf"} {
		branches, _ := schema[keyword].([]any)
		for _, branch := range branches {
			child, _ := branch.(map[string]any)
			collectSchemaSecrets(value, child, collect)
		}
	}
	switch value := value.(type) {
	case map[string]any:
		properties, _ := schema["properties"].(map[string]any)
		additional, _ := schema["additionalProperties"].(map[string]any)
		for key, item := range value {
			child, declared := properties[key].(map[string]any)
			if !declared {
				child = additional
			}
			collectSchemaSecrets(item, child, collect)
		}
	case []any:
		items, _ := schema["items"].(map[string]any)
		prefix, _ := schema["prefixItems"].([]any)
		if tuple, ok := schema["items"].([]any); ok {
			prefix = tuple
		}
		for i, item := range value {
			child := items
			if i < len(prefix) {
				child, _ = prefix[i].(map[string]any)
			}
			collectSchemaSecrets(item, child, collect)
		}
	}
}
