# Go plugin SDK

Import `github.com/xingexin/catbot/packages/plugin-sdk-go` as `pluginsdk`.
The SDK uses the repository's Go module and has no dependency on `internal/`.

```go
func main() {
    ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
    defer cancel()
    err := pluginsdk.Serve(ctx, map[string]pluginsdk.Handler{
        "hello": func(ctx context.Context, args map[string]any, tool pluginsdk.ToolContext) (any, error) {
            return map[string]any{"text": "Hello " + args["name"].(string)}, nil
        },
    })
    if err != nil && !errors.Is(err, context.Canceled) {
        fmt.Fprintln(os.Stderr, err)
        os.Exit(1)
    }
}
```

Declare the `hello` tool in `plugin.json` with an object input schema requiring
`name` to be a string. Set the manifest's runtime to `binary` and entry to the
compiled executable's relative path. Compile for the host OS and architecture;
Docker deployments need a Linux executable. See the repository's plugin guide
and `plugins/example-go` for a complete manifest and build example.

`Serve` loads `plugin.json` from the working directory and consumes
`SECRETARY_PLUGIN_CONFIG`, `SECRETARY_HOST_URL`, and `SECRETARY_HOST_TOKEN`.
The host sets these variables. Tool handlers receive a fresh configuration copy
and an authenticated `ToolContext.Host`. Keep logs on stderr: stdout is MCP.

Use `Host.Get/Set` for scoped storage, `Save` for structured results,
`Upload/Download` for files, `Generate/Transcribe` for models, and `Task/Notify`
for tasks and notifications. Host permissions in the manifest still apply.
Pass the handler's `ctx` to every call so cancellation reaches downstream work.
Use `tool.OperationID` plus a fixed suffix for each distinct task or notification;
retries must reuse that ID. The SDK makes no application-level retries.

Tool inputs and outputs are schema-validated. Scalars and arrays become
`{"value": ...}`. Results are limited to 512 KiB; persist larger output through
host artifacts and return its ID. Sensitive config values and the host token are
redacted from returned tool data/errors. This does not sanitize your own logs.

`NewServer` accepts explicit `Options` and returns an MCP server suitable for
in-memory transport tests. `NewHost` accepts an explicit HTTP client and size
limits. The default timeout is 180 seconds, JSON response limit is 2 MiB, and
file limit is 100 MiB; redirects are always rejected to avoid forwarding tokens.

Run tests from the repository root:

```sh
go test -race ./packages/plugin-sdk-go
```
