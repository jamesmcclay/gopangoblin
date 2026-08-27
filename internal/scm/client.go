// Package scm is a minimal client for the Palo Alto Networks Strata Cloud
// Manager (SCM) config APIs, shared by the gopangoblin tools that need it
// (habuilder, reset): resolving a device by serial number, and managing its
// HA configuration, management interface, DNS settings, and other
// folder/snippet/device-scoped config resources.
//
// API reference: https://pan.dev/scm/api/
package scm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	defaultTokenURL = "https://auth.apps.paloaltonetworks.com/oauth2/access_token"
	defaultAPIBase  = "https://api.strata.paloaltonetworks.com"
)

// Credentials are the OAuth2 client-credentials inputs for a SCM service account.
type Credentials struct {
	ClientID     string
	ClientSecret string
	TSGID        string
}

// Client talks to the SCM config APIs.
type Client struct {
	creds    Credentials
	tokenURL string
	apiBase  string
	http     *http.Client

	mu          sync.Mutex
	accessToken string
	expiresAt   time.Time
}

// Option configures a Client, mainly for tests.
type Option func(*Client)

// WithTokenURL overrides the OAuth2 token endpoint.
func WithTokenURL(u string) Option { return func(c *Client) { c.tokenURL = u } }

// WithAPIBase overrides the SCM config API base URL.
func WithAPIBase(u string) Option { return func(c *Client) { c.apiBase = u } }

// WithHTTPClient overrides the underlying *http.Client.
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.http = h } }

// NewClient builds a Client for the given service account credentials.
func NewClient(creds Credentials, opts ...Option) *Client {
	c := &Client{
		creds:    creds,
		tokenURL: defaultTokenURL,
		apiBase:  defaultAPIBase,
		http:     &http.Client{Timeout: 30 * time.Second},
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// APIError is returned when SCM responds with a non-2xx status. It carries
// the raw response body so callers/tests can surface the real error detail
// (SCM error bodies vary a lot by endpoint).
type APIError struct {
	StatusCode int
	Body       []byte
}

func (e *APIError) Error() string {
	return fmt.Sprintf("scm api: status %d: %s", e.StatusCode, strings.TrimSpace(string(e.Body)))
}

// IsNotFound reports whether err represents a "no HA configuration exists"
// response from SCM. The API is documented to return 404 for this, but in
// practice it returns 500 with an {"_errors":[{"code":"CH_0001",...}]} body
// instead, so both shapes are treated as not-found.
func IsNotFound(err error) bool {
	apiErr, ok := err.(*APIError)
	if !ok {
		return false
	}
	if apiErr.StatusCode == http.StatusNotFound {
		return true
	}

	var parsed struct {
		Errors []struct {
			Code string `json:"code"`
		} `json:"_errors"`
	}
	if json.Unmarshal(apiErr.Body, &parsed) == nil {
		for _, e := range parsed.Errors {
			if e.Code == "CH_0001" {
				return true
			}
		}
	}
	return false
}

func (c *Client) token() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.accessToken != "" && time.Now().Before(c.expiresAt) {
		return c.accessToken, nil
	}

	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	form.Set("scope", "tsg_id:"+c.creds.TSGID)

	req, err := http.NewRequest(http.MethodPost, c.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("building token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(c.creds.ClientID, c.creds.ClientSecret)

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("requesting access token: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("reading token response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("requesting access token: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var tok struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &tok); err != nil {
		return "", fmt.Errorf("parsing token response: %w", err)
	}
	if tok.AccessToken == "" {
		return "", fmt.Errorf("token response had no access_token: %s", strings.TrimSpace(string(body)))
	}

	c.accessToken = tok.AccessToken
	// Refresh a bit early to avoid racing expiry mid-request.
	c.expiresAt = time.Now().Add(time.Duration(tok.ExpiresIn)*time.Second - 30*time.Second)

	return c.accessToken, nil
}

// doJSON issues an authenticated request against the SCM config API. body
// (if non-nil) is marshaled as the JSON request body; out (if non-nil)
// receives the unmarshaled JSON response body.
func (c *Client) doJSON(method, path string, query url.Values, body, out interface{}) error {
	tok, err := c.token()
	if err != nil {
		return err
	}

	u := c.apiBase + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}

	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshaling request body: %w", err)
		}
		reqBody = bytes.NewReader(b)
	}

	req, err := http.NewRequest(method, u, reqBody)
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("%s %s: reading response: %w", method, path, err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &APIError{StatusCode: resp.StatusCode, Body: respBody}
	}

	if out != nil && len(bytes.TrimSpace(respBody)) > 0 {
		if err := json.Unmarshal(respBody, out); err != nil {
			return fmt.Errorf("%s %s: parsing response: %w", method, path, err)
		}
	}

	return nil
}
