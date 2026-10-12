package console

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Client shares the authenticated loopback API with the optional browser UI.
type Client struct {
	URL   string
	token string
	http  *http.Client
}

func NewClient(address, state string) (*Client, error) {
	u, err := url.Parse(address)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("daemon URL must be http://127.0.0.1:port")
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port < 1 || port > 65535 {
		return nil, errors.New("daemon URL needs a valid port")
	}
	b, err := os.ReadFile(filepath.Join(state, "dashboard-token"))
	if err != nil {
		return nil, fmt.Errorf("read daemon token: %w; for a system service use sudo linkweave tui", err)
	}
	token := strings.TrimSpace(string(b))
	if _, err = hex.DecodeString(token); err != nil || len(token) != 64 {
		return nil, errors.New("invalid daemon token")
	}
	return &Client{URL: strings.TrimRight(address, "/"), token: token, http: &http.Client{
		Timeout:       60 * time.Second,
		Transport:     &http.Transport{Proxy: nil},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

func (c *Client) DashboardURL() string { return c.URL + "/#token=" + c.token }

func (c *Client) Request(ctx context.Context, method, path string, body []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.URL+"/api/"+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if method == "POST" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, (2<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(b) > 2<<20 {
		return nil, errors.New("daemon response too large")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var failure struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(b, &failure) == nil && failure.Error != "" {
			return nil, errors.New(failure.Error)
		}
		return nil, fmt.Errorf("daemon HTTP %d", resp.StatusCode)
	}
	return b, nil
}
