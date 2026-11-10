// Package client implements the narrowly scoped, authenticated Factory 0.27 API.
// It must not be used as a general-purpose session, approval or execution client.
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
	"regexp"
	"strings"
	"sync"
	"time"
)

const maxResponseSize = 1024 * 1024

var skillNamePattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
var projectPattern = regexp.MustCompile(`^[a-fA-F0-9]{8}(-[a-fA-F0-9]{4}){3}-[a-fA-F0-9]{12}$`)

// Config accepts either a preexisting Better Auth session token or email/password
// for the Better Auth sign-in/email flow. Never supply credentials via Terraform state.
type Config struct {
	Endpoint, TrustedEndpoint, AuthOrigin, Email, Password, SessionToken string
	HTTPClient                                                           *http.Client
}

type Client struct {
	base            string
	authOrigin      string
	email, password string
	http            *http.Client
	mu              sync.Mutex
	token           string
}

func New(cfg Config) (*Client, error) {
	raw := strings.TrimSpace(cfg.Endpoint)
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" {
		return nil, errors.New("endpoint must be an origin URL without credentials, path, query or fragment")
	}
	switch u.Scheme {
	case "https":
	case "http":
		host := strings.ToLower(u.Hostname())
		ip := net.ParseIP(host)
		// Plaintext session credentials are only permitted on a loopback or
		// an internal Kubernetes service name (never a public IP/domain).
		if !(host == "localhost" || (ip != nil && ip.IsLoopback()) || strings.HasSuffix(host, ".svc") || strings.HasSuffix(host, ".svc.cluster.local")) {
			return nil, errors.New("HTTP is permitted only for loopback or Kubernetes Service DNS")
		}
	default:
		return nil, errors.New("endpoint must use HTTPS, or HTTP for loopback/Kubernetes Service DNS")
	}
	// Credentials are sent to the configured endpoint. Pin that destination
	// independently from HCL so a config change cannot redirect a credentialed
	// runner to an attacker-controlled HTTPS host.
	trustedRaw := strings.TrimSpace(cfg.TrustedEndpoint)
	trusted, trustedErr := url.Parse(trustedRaw)
	if trustedErr != nil || trusted == nil || trusted.Hostname() == "" || trusted.User != nil ||
		trusted.RawQuery != "" || trusted.Fragment != "" || trusted.Path != "" && trusted.Path != "/" ||
		trusted.Scheme != "https" && trusted.Scheme != "http" {
		return nil, errors.New("trusted endpoint must be an HTTP(S) origin without credentials, path, query or fragment")
	}
	if u.Scheme != trusted.Scheme || !strings.EqualFold(u.Host, trusted.Host) {
		return nil, errors.New("configured endpoint does not match the runner-pinned trusted endpoint")
	}
	// Better Auth validates Origin against the public trusted origins even when
	// the provider connects to an internal Kubernetes Service address.
	authOrigin := strings.TrimSpace(cfg.AuthOrigin)
	if authOrigin == "" {
		authOrigin = strings.TrimRight(u.String(), "/")
	}
	authURL, authErr := url.Parse(authOrigin)
	if authErr != nil || authURL == nil || authURL.Hostname() == "" || authURL.User != nil ||
		authURL.RawQuery != "" || authURL.Fragment != "" || authURL.Path != "" && authURL.Path != "/" ||
		authURL.Scheme != "https" && authURL.Scheme != "http" {
		return nil, errors.New("auth origin must be an HTTP(S) origin without credentials, path, query or fragment")
	}
	if authURL.Scheme == "http" && authURL.Host != u.Host {
		return nil, errors.New("plaintext auth origin must match the configured endpoint")
	}
	token := strings.TrimSpace(cfg.SessionToken)
	if token == "" && (strings.TrimSpace(cfg.Email) == "" || cfg.Password == "") {
		return nil, errors.New("provide session token or both service-account email and password")
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 25 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}}
	}
	return &Client{base: strings.TrimRight(u.String(), "/"), authOrigin: strings.TrimRight(authOrigin, "/"), email: cfg.Email, password: cfg.Password, token: token, http: cfg.HTTPClient}, nil
}

func validateProject(projectID string) error {
	if !projectPattern.MatchString(projectID) {
		return errors.New("invalid Factory project UUID")
	}
	return nil
}
func validateSkill(name string) error {
	if len(name) > 64 || !skillNamePattern.MatchString(name) {
		return errors.New("invalid Factory skill name")
	}
	return nil
}

// APIError is deliberately sanitized: request body, passwords and session
// tokens are never copied into Terraform diagnostics or CI logs.
type APIError struct {
	Method string
	Status int
}

func (e *APIError) Error() string {
	return fmt.Sprintf("Factory API %s failed (HTTP %d)", e.Method, e.Status)
}

func (c *Client) signIn(ctx context.Context) (string, error) {
	payload, _ := json.Marshal(map[string]any{"email": c.email, "password": c.password, "rememberMe": false})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/auth/api/sign-in/email", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", c.authOrigin)
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("Better Auth sign-in request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", &APIError{Method: "sign-in", Status: resp.StatusCode}
	}
	var data struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseSize)).Decode(&data); err != nil {
		return "", errors.New("Better Auth sign-in returned invalid JSON")
	}
	if data.Token == "" {
		return "", errors.New("Better Auth sign-in did not return a session token")
	}
	return data.Token, nil
}

func (c *Client) session(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" {
		return c.token, nil
	}
	if c.email == "" || c.password == "" {
		return "", errors.New("session token unavailable")
	}
	token, err := c.signIn(ctx)
	if err != nil {
		return "", err
	}
	c.token = token
	return token, nil
}

func (c *Client) call(ctx context.Context, method, path string, payload any, result any) error {
	token, err := c.session(ctx)
	if err != nil {
		return err
	}
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("Factory API request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &APIError{Method: method, Status: resp.StatusCode}
	}
	if result == nil {
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseSize)).Decode(result); err != nil {
		return fmt.Errorf("Factory API returned malformed %s response", method)
	}
	return nil
}

// Skill is the resolved representation returned by GET /web/factory/skills.
// The API omits raw SKILL.md and trims instructions; state should compare
// semantic description+instructions, not raw Markdown bytes.
type Skill struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Content     string `json:"content"`
	Origin      string `json:"origin"`
	Edited      *struct {
		UpdatedAt string `json:"updatedAt"`
	} `json:"edited"`
	OriginalChanged bool   `json:"originalChanged"`
	Error           string `json:"error"`
}

type Board struct {
	Key         string          `json:"key"`
	Name        string          `json:"name"`
	Description *string         `json:"description"`
	Source      string          `json:"source"`
	Editable    bool            `json:"editable"`
	Revision    *int64          `json:"revision"`
	Phases      json.RawMessage `json:"phases"`
	Problems    []string        `json:"problems"`
}

func (c *Client) Skill(ctx context.Context, projectID, name string) (*Skill, error) {
	if err := validateProject(projectID); err != nil {
		return nil, err
	}
	if err := validateSkill(name); err != nil {
		return nil, err
	}
	var response struct {
		Skills []Skill `json:"skills"`
	}
	err := c.call(ctx, http.MethodGet, "/web/factory/skills?projectId="+url.QueryEscape(projectID), nil, &response)
	if err != nil {
		return nil, err
	}
	for _, skill := range response.Skills {
		if skill.Name == name {
			if skill.Error != "" {
				return nil, fmt.Errorf("skill %q cannot be loaded: invalid remote instructions", name)
			}
			return &skill, nil
		}
	}
	return nil, fmt.Errorf("skill %q does not exist in Factory project %q", name, projectID)
}

func Markdown(name, description, instructions string) (string, error) {
	if err := validateSkill(name); err != nil {
		return "", err
	}
	if strings.TrimSpace(description) == "" || strings.TrimSpace(instructions) == "" {
		return "", errors.New("description and instructions must not be empty")
	}
	if strings.ContainsRune(description, '\x00') || strings.ContainsRune(instructions, '\x00') {
		return "", errors.New("skill fields contain NUL")
	}
	// JSON-quoted strings are valid YAML scalars; no YAML dependency needed.
	quoted, _ := json.Marshal(description)
	return "---\nname: " + name + "\ndescription: " + string(quoted) + "\n---\n\n" + strings.TrimSpace(instructions) + "\n", nil
}

func (c *Client) UpsertSkill(ctx context.Context, projectID, name, description, instructions string) error {
	if err := validateProject(projectID); err != nil {
		return err
	}
	md, err := Markdown(name, description, instructions)
	if err != nil {
		return err
	}
	return c.call(ctx, http.MethodPut, "/web/factory/projects/"+url.PathEscape(projectID)+"/skills/"+url.PathEscape(name), map[string]string{"content": md}, &struct{}{})
}
func (c *Client) DeleteSkillOverride(ctx context.Context, projectID, name string) error {
	if err := validateProject(projectID); err != nil {
		return err
	}
	if err := validateSkill(name); err != nil {
		return err
	}
	return c.call(ctx, http.MethodDelete, "/web/factory/projects/"+url.PathEscape(projectID)+"/skills/"+url.PathEscape(name), nil, &struct{}{})
}
func (c *Client) Board(ctx context.Context, projectID, boardKey string) (*Board, error) {
	if err := validateProject(projectID); err != nil {
		return nil, err
	}
	if boardKey == "" || len(boardKey) > 128 || strings.ContainsAny(boardKey, "/?#") {
		return nil, errors.New("invalid Factory board key")
	}
	var response struct {
		Board Board `json:"board"`
	}
	if err := c.call(ctx, http.MethodGet, "/web/factory/projects/"+url.PathEscape(projectID)+"/boards/"+url.PathEscape(boardKey), nil, &response); err != nil {
		return nil, err
	}
	if response.Board.Key != boardKey {
		return nil, errors.New("Factory board response key mismatch")
	}
	return &response.Board, nil
}
