// Package railwayapi is a client for Railway's backboard GraphQL API, covering
// the operations the IaC provider needs: reading an environment's config,
// previewing and applying change sets, and the lookups behind data sources.
package railwayapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// DefaultEndpoint is backboard's public GraphQL endpoint.
const DefaultEndpoint = "https://backboard.railway.com/graphql/v2"

const (
	defaultUserAgent    = "terraform-provider-railway-iac-sdk"
	defaultPollInterval = time.Second
	maxAttempts         = 5
	maxBackoff          = 30 * time.Second
	errorBodyExcerpt    = 512
)

// Credential selects how requests authenticate. Exactly one field must be set:
// Token is an account or workspace token sent as a bearer token, and
// ProjectToken is a project access token.
type Credential struct {
	Token        string
	ProjectToken string
}

// Client sends GraphQL requests to backboard.
type Client struct {
	endpoint     string
	httpClient   *http.Client
	userAgent    string
	pollInterval time.Duration
	retryBase    time.Duration
	headerName   string
	headerValue  string
}

// Option configures a Client.
type Option func(*Client)

// WithEndpoint overrides the GraphQL endpoint URL.
func WithEndpoint(url string) Option {
	return func(c *Client) { c.endpoint = url }
}

// WithHTTPClient sets the HTTP client used for requests. The client is copied
// and its redirect policy replaced so redirects are always refused.
func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) {
		if hc != nil {
			copied := *hc
			c.httpClient = &copied
		}
	}
}

// WithUserAgent sets the User-Agent header.
func WithUserAgent(ua string) Option {
	return func(c *Client) { c.userAgent = ua }
}

// WithPollInterval sets how long ApplyChangeSet waits between status polls.
func WithPollInterval(d time.Duration) Option {
	return func(c *Client) { c.pollInterval = d }
}

// New returns a Client authenticated with cred. It fails unless exactly one
// of cred.Token and cred.ProjectToken is set.
func New(cred Credential, opts ...Option) (*Client, error) {
	c := &Client{
		endpoint:     DefaultEndpoint,
		httpClient:   &http.Client{},
		userAgent:    defaultUserAgent,
		pollInterval: defaultPollInterval,
		retryBase:    500 * time.Millisecond,
	}
	switch {
	case cred.Token != "" && cred.ProjectToken != "":
		return nil, errors.New("railway credential: set either an account/workspace token or a project token, not both")
	case cred.Token != "":
		c.headerName, c.headerValue = "Authorization", "Bearer "+cred.Token
	case cred.ProjectToken != "":
		c.headerName, c.headerValue = "Project-Access-Token", cred.ProjectToken
	default:
		return nil, errors.New("railway credential: a token or project token is required")
	}
	for _, opt := range opts {
		opt(c)
	}
	c.httpClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		return errors.New("railway GraphQL redirects are not allowed")
	}
	if c.pollInterval <= 0 {
		c.pollInterval = defaultPollInterval
	}
	return c, nil
}

// GraphQLErrorItem is one entry of a GraphQL response's errors array.
type GraphQLErrorItem struct {
	Message    string         `json:"message"`
	Path       []any          `json:"path,omitempty"`
	Extensions map[string]any `json:"extensions,omitempty"`
}

// Code returns the item's extensions.code, or "" when absent.
func (e GraphQLErrorItem) Code() string {
	code, _ := e.Extensions["code"].(string)
	return code
}

// GraphQLError reports a response whose errors array was non-empty.
type GraphQLError struct {
	StatusCode int
	Errors     []GraphQLErrorItem
}

func (e *GraphQLError) Error() string {
	parts := make([]string, 0, len(e.Errors))
	for _, item := range e.Errors {
		msg := item.Message
		if len(item.Path) > 0 {
			segments := make([]string, len(item.Path))
			for i, p := range item.Path {
				segments[i] = fmt.Sprint(p)
			}
			msg += " (at " + strings.Join(segments, ".") + ")"
		}
		parts = append(parts, msg)
	}
	return "railway GraphQL error: " + strings.Join(parts, "; ")
}

// HTTPError reports a non-success HTTP status without a GraphQL errors body.
type HTTPError struct {
	StatusCode int
	Body       string
}

func (e *HTTPError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("railway API returned HTTP %d", e.StatusCode)
	}
	return fmt.Sprintf("railway API returned HTTP %d: %s", e.StatusCode, e.Body)
}

type response struct {
	Data   json.RawMessage    `json:"data"`
	Errors []GraphQLErrorItem `json:"errors"`
}

// Do posts query with variables and decodes the response's data into out,
// which may be nil. Responses with HTTP 429 or 5xx are retried with bounded
// exponential backoff that honours Retry-After. A non-empty errors array is
// returned as *GraphQLError.
func (c *Client) Do(ctx context.Context, query string, variables map[string]any, out any) error {
	if variables == nil {
		variables = map[string]any{}
	}
	body, err := json.Marshal(map[string]any{"query": query, "variables": variables})
	if err != nil {
		return fmt.Errorf("encode GraphQL request: %w", err)
	}

	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		status, header, payload, err := c.post(ctx, body)
		if err != nil {
			return err
		}
		if status == http.StatusTooManyRequests || status >= 500 {
			lastErr = httpOrGraphQLError(status, payload)
			if attempt == maxAttempts-1 {
				break
			}
			if err := sleep(ctx, c.backoff(attempt, header)); err != nil {
				return fmt.Errorf("%w (last response: %v)", err, lastErr)
			}
			continue
		}
		return decode(status, payload, out)
	}
	return fmt.Errorf("railway API request failed after %d attempts: %w", maxAttempts, lastErr)
}

func (c *Client) post(ctx context.Context, body []byte) (int, http.Header, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return 0, nil, nil, fmt.Errorf("build railway request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set(c.headerName, c.headerValue)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("railway API request: %w", redact(err, c.headerValue))
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("read railway API response: %w", err)
	}
	return resp.StatusCode, resp.Header, payload, nil
}

func decode(status int, payload []byte, out any) error {
	var resp response
	if err := json.Unmarshal(payload, &resp); err != nil {
		if status < 200 || status > 299 {
			return &HTTPError{StatusCode: status, Body: excerpt(payload)}
		}
		return fmt.Errorf("decode railway API response: %w", err)
	}
	if len(resp.Errors) > 0 {
		return &GraphQLError{StatusCode: status, Errors: resp.Errors}
	}
	if status < 200 || status > 299 {
		return &HTTPError{StatusCode: status, Body: excerpt(payload)}
	}
	if len(resp.Data) == 0 || string(resp.Data) == "null" {
		return errors.New("railway API response has no data")
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(resp.Data, out); err != nil {
		return fmt.Errorf("decode railway API data: %w", err)
	}
	return nil
}

func httpOrGraphQLError(status int, payload []byte) error {
	var resp response
	if json.Unmarshal(payload, &resp) == nil && len(resp.Errors) > 0 {
		return &GraphQLError{StatusCode: status, Errors: resp.Errors}
	}
	return &HTTPError{StatusCode: status, Body: excerpt(payload)}
}

func excerpt(payload []byte) string {
	s := strings.TrimSpace(string(payload))
	if len(s) > errorBodyExcerpt {
		s = s[:errorBodyExcerpt] + "…"
	}
	return s
}

func (c *Client) backoff(attempt int, header http.Header) time.Duration {
	if wait, ok := retryAfter(header.Get("Retry-After")); ok {
		return min(wait, maxBackoff)
	}
	return min(c.retryBase<<attempt, maxBackoff)
}

func retryAfter(value string) (time.Duration, bool) {
	if value == "" {
		return 0, false
	}
	if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second, true
	}
	if at, err := http.ParseTime(value); err == nil {
		return max(time.Until(at), 0), true
	}
	return 0, false
}

func sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// redactedError hides a secret that a transport error might echo.
type redactedError struct {
	msg string
	err error
}

func (e *redactedError) Error() string { return e.msg }
func (e *redactedError) Unwrap() error { return e.err }

func redact(err error, secret string) error {
	if secret == "" || !strings.Contains(err.Error(), secret) {
		return err
	}
	return &redactedError{msg: strings.ReplaceAll(err.Error(), secret, "[REDACTED]"), err: err}
}
