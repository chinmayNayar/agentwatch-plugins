// Package client talks to the AgentWatch API. Every call has a deadline; no
// error string ever contains the token.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"time"
)

// Version is set at build time (-ldflags -X).
var Version = "0.0.0-dev"

// Client is a minimal AgentWatch API client.
type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

// New returns a client with a transport tuned for short hook calls.
func New(baseURL, token string) *Client {
	tr := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 3 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   3 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
		MaxIdleConns:          4,
		IdleConnTimeout:       30 * time.Second,
		ForceAttemptHTTP2:     true,
	}
	return &Client{BaseURL: strings.TrimRight(baseURL, "/"), Token: token, HTTP: &http.Client{Transport: tr}}
}

// Error is a non-2xx answer.
type Error struct {
	Status int
	Code   string // detail.code / detail.error when the API sends one
	Body   string // truncated, never includes request headers
}

func (e *Error) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("HTTP %d (%s)", e.Status, e.Code)
	}
	return fmt.Sprintf("HTTP %d", e.Status)
}

// ErrUnavailable wraps network errors and timeouts.
var ErrUnavailable = errors.New("AgentWatch API unavailable")

// IsUnavailable reports whether err means "could not get an answer in time":
// network, timeout, 5xx, 429 or an unreadable body.
func IsUnavailable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrUnavailable) {
		return true
	}
	var e *Error
	if errors.As(err, &e) {
		return e.Status >= 500 || e.Status == 429 || e.Status == 408
	}
	return false
}

// StatusOf returns the HTTP status of an *Error, or 0.
func StatusOf(err error) int {
	var e *Error
	if errors.As(err, &e) {
		return e.Status
	}
	return 0
}

// Do sends a JSON request and decodes a JSON answer into out (may be nil).
func (c *Client) Do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, body)
	if err != nil {
		return fmt.Errorf("bad request: %w", scrubURLErr(err))
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", fmt.Sprintf("agentwatch-sensor/%s (%s; %s)", Version, runtime.GOOS, runtime.GOARCH))
	if c.Token != "" {
		req.Header.Set("x-api-key", c.Token)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrUnavailable, describeNetErr(err))
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return fmt.Errorf("%w: reading body", ErrUnavailable)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		e := &Error{Status: resp.StatusCode, Body: truncate(string(raw), 300)}
		var d struct {
			Detail any `json:"detail"`
		}
		if json.Unmarshal(raw, &d) == nil {
			switch t := d.Detail.(type) {
			case map[string]any:
				for _, k := range []string{"code", "error"} {
					if s, ok := t[k].(string); ok {
						e.Code = s
						break
					}
				}
			case string:
				e.Code = truncate(t, 80)
			}
		}
		return e
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("%w: invalid JSON from API", ErrUnavailable)
	}
	return nil
}

func describeNetErr(err error) string {
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return "timeout"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Op + " " + redactHost(ue.URL) + ": " + scrubURLErr(ue.Err).Error()
	}
	return "network error"
}

func redactHost(u string) string {
	p, err := url.Parse(u)
	if err != nil {
		return "?"
	}
	return p.Scheme + "://" + p.Host + p.Path
}

func scrubURLErr(err error) error {
	if err == nil {
		return errors.New("")
	}
	s := err.Error()
	if i := strings.Index(s, "?"); i > 0 {
		s = s[:i]
	}
	return errors.New(s)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// ---- typed calls -----------------------------------------------------------

// Policy is GET /devices/policy.
type Policy struct {
	AgentName              string   `json:"agent_name"`
	PassportID             string   `json:"passport_id"`
	Status                 string   `json:"status"`
	PolicyMode             string   `json:"policy_mode"`
	EnforceMode            string   `json:"enforce_mode"`
	AllowedTools           []string `json:"allowed_tools"`
	DeniedTools            []string `json:"denied_tools"`
	ApprovalRequiredTools  []string `json:"approval_required_tools"`
	Isolated               bool     `json:"isolated"`
	ContentCapture         bool     `json:"content_capture"`
	ContentCaptureApprover string   `json:"content_capture_approved_by"`
	ETag                   string   `json:"etag"`
	FetchedAt              int64    `json:"fetched_at,omitempty"`
}

func (c *Client) Policy(ctx context.Context, agent string) (*Policy, error) {
	var p Policy
	err := c.Do(ctx, http.MethodGet, "/devices/policy?agent_name="+url.QueryEscape(agent), nil, &p)
	if err != nil {
		return nil, err
	}
	p.FetchedAt = time.Now().UnixMilli()
	return &p, nil
}

// RunIn is POST /runs.
type RunIn struct {
	AgentName    string   `json:"agent_name"`
	SessionID    string   `json:"session_id"`
	AgentVersion string   `json:"agent_version,omitempty"`
	Tags         []string `json:"tags,omitempty"`
}

type RunOut struct {
	RunID  string `json:"run_id"`
	Status string `json:"status"`
}

func (c *Client) CreateRun(ctx context.Context, in RunIn) (*RunOut, error) {
	var out RunOut
	return &out, c.Do(ctx, http.MethodPost, "/runs", in, &out)
}

// PreflightIn is POST /steps/preflight (sensor fields).
type PreflightIn struct {
	RunID              string         `json:"run_id"`
	StepType           string         `json:"step_type"`
	ToolName           string         `json:"tool_name,omitempty"`
	InputData          map[string]any `json:"input_data,omitempty"`
	NetworkDestination string         `json:"network_destination,omitempty"`
	Client             string         `json:"client"`
	Command            string         `json:"command,omitempty"`
	Path               string         `json:"path,omitempty"`
	MCPServer          string         `json:"mcp_server,omitempty"`
	MCPTool            string         `json:"mcp_tool,omitempty"`
	HoldID             string         `json:"hold_id,omitempty"`
	SensorMode         string         `json:"sensor_mode"`
}

type PreflightOut struct {
	Decision      string         `json:"decision"`
	Reasons       []string       `json:"reasons"`
	Mode          string         `json:"mode"`
	WouldHave     string         `json:"would_have"`
	HoldID        string         `json:"hold_id"`
	DisplayReason string         `json:"display_reason"`
	ScrubbedInput map[string]any `json:"scrubbed_input"`
}

func (c *Client) Preflight(ctx context.Context, in PreflightIn) (*PreflightOut, error) {
	var out PreflightOut
	return &out, c.Do(ctx, http.MethodPost, "/steps/preflight", in, &out)
}

// IngestIn is POST /ingest.
type IngestIn struct {
	SessionID    string         `json:"session_id"`
	AgentName    string         `json:"agent_name"`
	EventType    string         `json:"event_type"`
	ToolName     string         `json:"tool_name,omitempty"`
	Model        string         `json:"model,omitempty"`
	InputData    map[string]any `json:"input_data,omitempty"`
	OutputData   map[string]any `json:"output_data,omitempty"`
	ErrorType    string         `json:"error_type,omitempty"`
	ErrorMessage string         `json:"error_message,omitempty"`
	AgentVersion string         `json:"agent_version,omitempty"`
	Tags         []string       `json:"tags,omitempty"`
	EndRun       bool           `json:"end_run,omitempty"`
	EndStatus    string         `json:"end_status,omitempty"`
	EventID      string         `json:"event_id,omitempty"`
	LatencyMS    *int           `json:"latency_ms,omitempty"`
}

func (c *Client) Ingest(ctx context.Context, in IngestIn) error {
	return c.Do(ctx, http.MethodPost, "/ingest", in, nil)
}

// SensorEvent is one item of POST /devices/events.
type SensorEvent struct {
	Type      string         `json:"type"`
	AgentName string         `json:"agent_name,omitempty"`
	SessionID string         `json:"session_id,omitempty"`
	Detail    map[string]any `json:"detail,omitempty"`
}

func (c *Client) Events(ctx context.Context, deviceName string, evs []SensorEvent) error {
	return c.Do(ctx, http.MethodPost, "/devices/events", map[string]any{"events": evs, "device_name": deviceName}, nil)
}

// Heartbeat is POST /devices/heartbeat.
type ClientState struct {
	Client    string `json:"client"`
	Version   string `json:"version,omitempty"`
	Installed bool   `json:"installed"`
	HooksOK   bool   `json:"hooks_ok"`
	Scope     string `json:"scope,omitempty"`
}

type HeartbeatIn struct {
	DeviceName    string        `json:"device_name,omitempty"`
	UserLabel     string        `json:"user_label,omitempty"`
	OS            string        `json:"os,omitempty"`
	SensorVersion string        `json:"sensor_version"`
	Mode          string        `json:"mode"`
	FailMode      string        `json:"fail_mode"`
	Clients       []ClientState `json:"clients"`
	SpoolDropped  int           `json:"spool_dropped"`
	SpoolPending  int           `json:"spool_pending"`
}

func (c *Client) Heartbeat(ctx context.Context, in HeartbeatIn) error {
	return c.Do(ctx, http.MethodPost, "/devices/heartbeat", in, nil)
}

// Device code flow.
type DeviceCode struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	Interval                int    `json:"interval"`
	ExpiresIn               int    `json:"expires_in"`
}

func (c *Client) StartDeviceCode(ctx context.Context, name, label, osArch string) (*DeviceCode, error) {
	var out DeviceCode
	return &out, c.Do(ctx, http.MethodPost, "/devices/code",
		map[string]string{"device_name": name, "user_label": label, "os": osArch}, &out)
}

type DeviceToken struct {
	AccessToken string `json:"access_token"`
	DeviceID    string `json:"device_id"`
	UserLabel   string `json:"user_label"`
}

func (c *Client) PollDeviceToken(ctx context.Context, deviceCode string) (*DeviceToken, error) {
	var out DeviceToken
	return &out, c.Do(ctx, http.MethodPost, "/devices/token", map[string]string{"device_code": deviceCode}, &out)
}
