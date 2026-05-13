package fleet

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client speaks the fleet HTTP protocol against a remote peer. It is bound to
// a single shared bearer token; one Client instance is safe for use across
// peers.
type Client struct {
	HTTP  *http.Client
	Token string
}

// NewClient returns a Client configured with sensible request timeouts. Pass
// an existing *http.Client (e.g. for tests) or nil for the default.
func NewClient(token string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	return &Client{HTTP: httpClient, Token: token}
}

// Join POSTs the local Peer to baseURL's /fleet/join endpoint and returns the
// roster the peer reports back.
func (c *Client) Join(ctx context.Context, baseURL string, self Peer) ([]Peer, error) {
	endpoint, err := joinURL(baseURL, "/fleet/join")
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(JoinRequest{Peer: self})
	if err != nil {
		return nil, fmt.Errorf("marshal join: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("new join request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("post join %s: %w", endpoint, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("join %s: status %d: %s", endpoint, resp.StatusCode, strings.TrimSpace(string(snippet)))
	}
	var out JoinResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode join response: %w", err)
	}
	return out.Peers, nil
}

// StreamEvents subscribes to baseURL's /fleet/events SSE channel and invokes
// onEvent for each parsed event. It blocks until ctx is cancelled or the
// stream errors. The caller is expected to retry on error.
func (c *Client) StreamEvents(ctx context.Context, baseURL string, onEvent func(Event)) error {
	endpoint, err := joinURL(baseURL, "/fleet/events")
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("new events request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "text/event-stream")

	// SSE is long-lived; bypass the per-request timeout on the shared client.
	streamClient := *c.HTTP
	streamClient.Timeout = 0
	resp, err := streamClient.Do(req)
	if err != nil {
		return fmt.Errorf("get events %s: %w", endpoint, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("events %s: status %d: %s", endpoint, resp.StatusCode, strings.TrimSpace(string(snippet)))
	}

	reader := bufio.NewReader(resp.Body)
	var (
		evtType string
		dataBuf bytes.Buffer
	)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err == io.EOF {
				return io.EOF
			}
			return fmt.Errorf("read sse: %w", err)
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			if dataBuf.Len() == 0 {
				continue
			}
			var p Peer
			if err := json.Unmarshal(dataBuf.Bytes(), &p); err == nil {
				onEvent(Event{Type: EventType(evtType), Peer: p})
			}
			evtType = ""
			dataBuf.Reset()
			continue
		}
		switch {
		case strings.HasPrefix(line, "event:"):
			evtType = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			chunk := strings.TrimPrefix(line, "data:")
			chunk = strings.TrimPrefix(chunk, " ") // SSE spec: optional single leading space
			if dataBuf.Len() > 0 {
				dataBuf.WriteByte('\n')
			}
			dataBuf.WriteString(chunk)
		}
	}
}

func joinURL(base, path string) (string, error) {
	u, err := url.Parse(strings.TrimRight(base, "/"))
	if err != nil {
		return "", fmt.Errorf("parse base url %q: %w", base, err)
	}
	if u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("base url %q must include scheme and host", base)
	}
	u.Path = strings.TrimRight(u.Path, "/") + path
	return u.String(), nil
}

// normalizeBaseURL returns scheme://host[:port] with no path or trailing slash.
// Used to compare two advertise URLs for identity.
func normalizeBaseURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimRight(raw, "/"))
	if err != nil {
		return "", fmt.Errorf("parse url %q: %w", raw, err)
	}
	if u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("url %q missing scheme or host", raw)
	}
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host), nil
}
