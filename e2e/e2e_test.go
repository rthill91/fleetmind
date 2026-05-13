//go:build linux

// Package e2e contains integration tests that exercise a live FleetMind
// server in-process using an ephemeral listener. These tests validate the
// full path from HTTP transport → bearer auth → MCP JSON-RPC → procfs/sysfs
// parsers on the local Linux host.
//
// The Go SDK's Streamable HTTP client is used for the MCP layer so that the
// tests exercise the same code paths (negotiation, session management,
// structured output) as real-world MCP consumers.
package e2e

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/gjolly/fleetmind/internal/mcpserver"
)

// testToken is the fixed bearer token used by the test server.
const testToken = "test-e2e-token"

// startTestServer creates a FleetMind instance on an ephemeral TCP port.
// It returns the base HTTP URL (e.g. "http://127.0.0.1:12345") and a cancel
// function that shuts the server down gracefully.
func startTestServer(t *testing.T) (baseURL string, cancel func()) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("FleetMind only supports Linux hosts")
	}

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := mcpserver.New(mcpserver.Config{
		Listener: l,
		Token:    testToken,
		Version:  "0.0.0+e2e",
		Logger:   logger,
	})

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		// Errors during normal shutdown (e.g. listener close) are benign.
		_ = srv.Serve(ctx)
	}()

	baseURL = "http://" + l.Addr().String()
	waitForServer(t, baseURL)
	return baseURL, cancel
}

// waitForServer polls /healthz until it returns HTTP 200 or the deadline expires.
func waitForServer(t *testing.T, baseURL string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client := &http.Client{Timeout: 200 * time.Millisecond}
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/healthz", nil)
		if err != nil {
			t.Fatalf("build request: %v", err)
		}
		resp, err := client.Do(req)
		if err == nil && resp.StatusCode == http.StatusOK {
			resp.Body.Close()
			return
		}
		if resp != nil {
			resp.Body.Close()
		}
		select {
		case <-ctx.Done():
			t.Fatalf("server did not become ready: %v", ctx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// bearerClient returns an http.Client that injects the test bearer token.
func bearerClient(token string) *http.Client {
	return &http.Client{
		Transport: &authRoundTripper{
			base:  http.DefaultTransport.(*http.Transport).Clone(),
			token: token,
		},
		Timeout: 10 * time.Second,
	}
}

type authRoundTripper struct {
	base  *http.Transport
	token string
}

func (rt *authRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	// Clone so the original request is not mutated across retries.
	c := req.Clone(req.Context())
	c.Header.Set("Authorization", "Bearer "+rt.token)
	return rt.base.RoundTrip(c)
}

// connectMCP creates an MCP client session against the test server.
// The caller must eventually call session.Close().
func connectMCP(t *testing.T, baseURL string) *mcp.ClientSession {
	t.Helper()
	transport := &mcp.StreamableClientTransport{
		Endpoint:             baseURL + "/mcp",
		HTTPClient:           bearerClient(testToken),
		DisableStandaloneSSE: true, // tests are request/response only
	}

	client := mcp.NewClient(&mcp.Implementation{
		Name:    "fleetmind-e2e-client",
		Version: "0.0.0+e2e",
	}, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		t.Fatalf("mcp connect: %v", err)
	}
	return session
}

func TestHealthz(t *testing.T) {
	baseURL, cancel := startTestServer(t)
	defer cancel()

	resp, err := http.Get(baseURL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "ok\n" {
		t.Fatalf("body = %q, want \"ok\\n\"", string(body))
	}
}

func TestAuthMissing(t *testing.T) {
	baseURL, cancel := startTestServer(t)
	defer cancel()

	resp, err := http.Post(baseURL+"/mcp", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("POST /mcp: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
	if resp.Header.Get("WWW-Authenticate") == "" {
		t.Fatal("missing WWW-Authenticate header on 401")
	}
}

func TestAuthWrongToken(t *testing.T) {
	baseURL, cancel := startTestServer(t)
	defer cancel()

	req, _ := http.NewRequest(http.MethodPost, baseURL+"/mcp", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer wrong-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /mcp: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

func TestMCPInitialize(t *testing.T) {
	baseURL, cancel := startTestServer(t)
	defer cancel()

	session := connectMCP(t, baseURL)
	defer session.Close()

	ir := session.InitializeResult()
	if ir == nil {
		t.Fatal("initialize result is nil")
	}
	if ir.ServerInfo == nil || ir.ServerInfo.Name != "fleetmind" {
		t.Fatalf("server info mismatch: %+v", ir.ServerInfo)
	}
}

func TestToolsList(t *testing.T) {
	baseURL, cancel := startTestServer(t)
	defer cancel()

	session := connectMCP(t, baseURL)
	defer session.Close()

	ctx, cancel2 := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel2()

	result, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	if len(result.Tools) == 0 {
		t.Fatal("no tools registered")
	}

	want := map[string]bool{
		"system_info":             true,
		"cpu_info":                true,
		"memory_info":             true,
		"load_info":               true,
		"list_processes":          true,
		"get_process":             true,
		"list_block_devices":      true,
		"list_mounts":             true,
		"list_network_interfaces": true,
		"list_sockets":            true,
		"list_pci_devices":        true,
		"list_usb_devices":        true,
		"kernel_info":             true,
		"list_kernel_modules":     true,
		"list_dmi":                true,
		"list_sensors":            true,
		"read_journal":            true,
		"read_dmesg":              true,
	}

	got := map[string]bool{}
	for _, tool := range result.Tools {
		got[tool.Name] = true
	}
	for name := range want {
		if !got[name] {
			t.Errorf("missing expected tool %q", name)
		}
	}
}

func TestCallSystemInfo(t *testing.T) {
	baseURL, cancel := startTestServer(t)
	defer cancel()

	session := connectMCP(t, baseURL)
	defer session.Close()

	ctx, cancel2 := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel2()

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "system_info",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("call system_info: %v", err)
	}
	if result.IsError {
		t.Fatalf("system_info returned error: %v", result.Content)
	}
	if len(result.Content) == 0 {
		t.Fatal("system_info returned no content")
	}

	// Structured output should also be populated.
	if result.StructuredContent == nil {
		t.Fatal("system_info returned no structured content")
	}
}

func TestCallCPUInfo(t *testing.T) {
	baseURL, cancel := startTestServer(t)
	defer cancel()

	session := connectMCP(t, baseURL)
	defer session.Close()

	ctx, cancel2 := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel2()

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "cpu_info",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("call cpu_info: %v", err)
	}
	if result.IsError {
		t.Fatalf("cpu_info returned error: %v", result.Content)
	}
	if result.StructuredContent == nil {
		t.Fatal("cpu_info returned no structured content")
	}
}

func TestCallMemoryInfo(t *testing.T) {
	baseURL, cancel := startTestServer(t)
	defer cancel()

	session := connectMCP(t, baseURL)
	defer session.Close()

	ctx, cancel2 := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel2()

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "memory_info",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("call memory_info: %v", err)
	}
	if result.IsError {
		t.Fatalf("memory_info returned error: %v", result.Content)
	}
	if result.StructuredContent == nil {
		t.Fatal("memory_info returned no structured content")
	}
}

func TestCallProcessList(t *testing.T) {
	baseURL, cancel := startTestServer(t)
	defer cancel()

	session := connectMCP(t, baseURL)
	defer session.Close()

	ctx, cancel2 := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel2()

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "list_processes",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("call list_processes: %v", err)
	}
	if result.IsError {
		t.Fatalf("list_processes returned error: %v", result.Content)
	}
	if result.StructuredContent == nil {
		t.Fatal("list_processes returned no structured content")
	}
}

func TestCallGetProcessPID1(t *testing.T) {
	baseURL, cancel := startTestServer(t)
	defer cancel()

	session := connectMCP(t, baseURL)
	defer session.Close()

	ctx, cancel2 := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel2()

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "get_process",
		Arguments: map[string]any{
			"pid": float64(1), // JSON numbers unmarshal to float64 by default
		},
	})
	if err != nil {
		t.Fatalf("call get_process: %v", err)
	}
	if result.IsError {
		t.Fatalf("get_process returned error: %v", result.Content)
	}
	if result.StructuredContent == nil {
		t.Fatal("get_process returned no structured content")
	}
}

func TestCallMounts(t *testing.T) {
	baseURL, cancel := startTestServer(t)
	defer cancel()

	session := connectMCP(t, baseURL)
	defer session.Close()

	ctx, cancel2 := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel2()

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "list_mounts",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("call list_mounts: %v", err)
	}
	if result.IsError {
		t.Fatalf("list_mounts returned error: %v", result.Content)
	}
	if result.StructuredContent == nil {
		t.Fatal("list_mounts returned no structured content")
	}
}

func TestCallNetworkInterfaces(t *testing.T) {
	baseURL, cancel := startTestServer(t)
	defer cancel()

	session := connectMCP(t, baseURL)
	defer session.Close()

	ctx, cancel2 := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel2()

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "list_network_interfaces",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("call list_network_interfaces: %v", err)
	}
	if result.IsError {
		t.Fatalf("list_network_interfaces returned error: %v", result.Content)
	}
	if result.StructuredContent == nil {
		t.Fatal("list_network_interfaces returned no structured content")
	}
}

func TestCallKernelInfo(t *testing.T) {
	baseURL, cancel := startTestServer(t)
	defer cancel()

	session := connectMCP(t, baseURL)
	defer session.Close()

	ctx, cancel2 := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel2()

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "kernel_info",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("call kernel_info: %v", err)
	}
	if result.IsError {
		t.Fatalf("kernel_info returned error: %v", result.Content)
	}
	if result.StructuredContent == nil {
		t.Fatal("kernel_info returned no structured content")
	}
}

func TestCallBlockDevices(t *testing.T) {
	baseURL, cancel := startTestServer(t)
	defer cancel()

	session := connectMCP(t, baseURL)
	defer session.Close()

	ctx, cancel2 := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel2()

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "list_block_devices",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("call list_block_devices: %v", err)
	}
	if result.IsError {
		t.Fatalf("list_block_devices returned error: %v", result.Content)
	}
	if result.StructuredContent == nil {
		t.Fatal("list_block_devices returned no structured content")
	}
}

func TestCallPCIandUSB(t *testing.T) {
	baseURL, cancel := startTestServer(t)
	defer cancel()

	session := connectMCP(t, baseURL)
	defer session.Close()

	ctx, cancel2 := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel2()

	for _, name := range []string{"list_pci_devices", "list_usb_devices"} {
		result, err := session.CallTool(ctx, &mcp.CallToolParams{
			Name:      name,
			Arguments: map[string]any{},
		})
		if err != nil {
			t.Fatalf("call %s: %v", name, err)
		}
		if result.IsError {
			t.Fatalf("%s returned error (this may be expected on systems without PCI/USB)", name)
		}
	}
}

func TestCallDMISensors(t *testing.T) {
	baseURL, cancel := startTestServer(t)
	defer cancel()

	session := connectMCP(t, baseURL)
	defer session.Close()

	ctx, cancel2 := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel2()

	for _, name := range []string{"list_dmi", "list_sensors"} {
		result, err := session.CallTool(ctx, &mcp.CallToolParams{
			Name:      name,
			Arguments: map[string]any{},
		})
		if err != nil {
			t.Fatalf("call %s: %v", name, err)
		}
		if result.IsError {
			t.Fatalf("%s returned error (this may be expected on virtualized systems without DMI/hwmon)", name)
		}
	}
}
