package mcpserver

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/1broseidon/mc/internal/config"
	"github.com/1broseidon/mc/internal/contract"
)

func TestServerListsMVPTools(t *testing.T) {
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	server := New(Options{
		Version: contract.VersionInfo{Version: "test", Commit: "test", Built: "test"},
		Config:  config.Effective{ScreenshotDir: t.TempDir(), Sources: map[string]string{}},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		errCh <- server.Run(ctx, serverTransport)
	}()

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "test"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect failed: %v", err)
	}
	defer func() { _ = session.Close() }()

	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools failed: %v", err)
	}
	got := map[string]bool{}
	for _, tool := range tools.Tools {
		got[tool.Name] = true
	}
	for _, want := range []string{"doctor", "get_screen_info", "list_windows", "focused_window", "observe", "screenshot", "focus_window", "move_mouse", "click", "drag", "scroll", "type_text", "press_key", "set_text", "perform_action", "computer_actions", "browser_session", "browser_pipeline"} {
		if !got[want] {
			t.Fatalf("tool %q missing from tools/list", want)
		}
	}
	toolsByName := map[string]*mcp.Tool{}
	for i := range tools.Tools {
		tool := tools.Tools[i]
		toolsByName[tool.Name] = tool
	}
	assertSchemaContains(t, toolsByName["screenshot"], "max_edge", "jpeg_quality", "cursor")
	assertSchemaExcludes(t, toolsByName["screenshot"], "MaxEdge", "JPEGQuality")
	assertSchemaContains(t, toolsByName["perform_action"], "element_id")
	assertSchemaContains(t, toolsByName["browser_session"], "launch", "headless", "browser_bin")
	cancel()
	<-errCh
}

func TestComputerActionsRejectsMissingSchemaVersion(t *testing.T) {
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	server := New(Options{
		Version: contract.VersionInfo{Version: "test", Commit: "test", Built: "test"},
		Config:  config.Effective{ScreenshotDir: t.TempDir(), Sources: map[string]string{}, RespectUser: true},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- server.Run(ctx, serverTransport) }()

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "test"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect failed: %v", err)
	}
	defer func() { _ = session.Close() }()

	// Intentionally omit schema_version. Send one harmless action so we
	// exercise the validator path that runs before the action loop.
	args := map[string]any{
		"actions": []map[string]any{
			{"type": "wait", "duration_ms": 1},
		},
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "computer_actions", Arguments: args})
	if err != nil {
		t.Fatalf("CallTool errored at the transport: %v", err)
	}
	if !result.IsError {
		t.Fatalf("expected tool result to be flagged as error, got success: %+v", result)
	}
	var bodyText string
	for _, c := range result.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			bodyText += tc.Text
		}
	}
	if !strings.Contains(bodyText, "VALIDATION_SCHEMA_VERSION_REQUIRED") {
		t.Fatalf("expected VALIDATION_SCHEMA_VERSION_REQUIRED in result body: %s", bodyText)
	}
	if !strings.Contains(bodyText, "remediation") && !strings.Contains(bodyText, "schema_version") {
		t.Fatalf("expected a remediation hint in the error body: %s", bodyText)
	}

	cancel()
	<-errCh
}

func TestToolAppErrorsUseMachineReadableBody(t *testing.T) {
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	server := New(Options{
		Version: contract.VersionInfo{Version: "test", Commit: "test", Built: "test"},
		Config:  config.Effective{ScreenshotDir: t.TempDir(), Sources: map[string]string{}},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- server.Run(ctx, serverTransport) }()

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "test"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect failed: %v", err)
	}
	defer func() { _ = session.Close() }()

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "paste",
		Arguments: map[string]any{"method": "bogus"},
	})
	if err != nil {
		t.Fatalf("CallTool should return a tool error body, not transport error: %v", err)
	}
	if !result.IsError {
		t.Fatalf("expected tool result to be flagged as error, got success: %+v", result)
	}
	var bodyText string
	for _, c := range result.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			bodyText += tc.Text
		}
	}
	if !strings.Contains(bodyText, `"code":"PASTE_METHOD_INVALID"`) {
		t.Fatalf("expected machine-readable AppError JSON in result body: %s", bodyText)
	}

	cancel()
	<-errCh
}

func TestSupportedSchemaVersionsExported(t *testing.T) {
	got := SupportedSchemaVersions()
	if len(got) == 0 {
		t.Fatal("SupportedSchemaVersions must be non-empty")
	}
	info := GetServerInfo(contract.VersionInfo{Version: "test"})
	if len(info.SchemaVersions) == 0 {
		t.Fatal("GetServerInfo must advertise schema_versions")
	}
}

func assertSchemaContains(t *testing.T, tool *mcp.Tool, wants ...string) {
	t.Helper()
	b, err := json.Marshal(tool.InputSchema)
	if err != nil {
		t.Fatalf("marshal %s input schema failed: %v", tool.Name, err)
	}
	s := string(b)
	for _, want := range wants {
		if !strings.Contains(s, want) {
			t.Fatalf("%s input schema missing %q: %s", tool.Name, want, s)
		}
	}
}

func assertSchemaExcludes(t *testing.T, tool *mcp.Tool, rejects ...string) {
	t.Helper()
	b, err := json.Marshal(tool.InputSchema)
	if err != nil {
		t.Fatalf("marshal %s input schema failed: %v", tool.Name, err)
	}
	s := string(b)
	for _, reject := range rejects {
		if strings.Contains(s, reject) {
			t.Fatalf("%s input schema leaked %q: %s", tool.Name, reject, s)
		}
	}
}
