package railwayapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const testToken = "secret-account-token-123"

type recordedRequest struct {
	Header    http.Header
	Query     string
	Variables map[string]any
}

type fakeBackboard struct {
	t        *testing.T
	mu       sync.Mutex
	requests []recordedRequest
	respond  func(n int, req recordedRequest, w http.ResponseWriter)
	server   *httptest.Server
}

func newFake(t *testing.T, respond func(n int, req recordedRequest, w http.ResponseWriter)) *fakeBackboard {
	t.Helper()
	f := &fakeBackboard{t: t, respond: respond}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var payload struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if r.Method != http.MethodPost || json.Unmarshal(body, &payload) != nil {
			t.Errorf("unexpected request %s %q", r.Method, body)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		req := recordedRequest{Header: r.Header.Clone(), Query: payload.Query, Variables: payload.Variables}
		f.mu.Lock()
		n := len(f.requests)
		f.requests = append(f.requests, req)
		f.mu.Unlock()
		f.respond(n, req, w)
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeBackboard) client(t *testing.T, cred Credential, opts ...Option) *Client {
	t.Helper()
	c, err := New(cred, append([]Option{WithEndpoint(f.server.URL), WithPollInterval(time.Millisecond)}, opts...)...)
	if err != nil {
		t.Fatal(err)
	}
	c.retryBase = time.Millisecond
	return c
}

func (f *fakeBackboard) recorded() []recordedRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recordedRequest(nil), f.requests...)
}

func writeJSON(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestNewRequiresExactlyOneCredential(t *testing.T) {
	if _, err := New(Credential{}); err == nil {
		t.Error("New accepted an empty credential")
	}
	_, err := New(Credential{Token: testToken, ProjectToken: "project-token"})
	if err == nil {
		t.Fatal("New accepted both credentials")
	}
	if strings.Contains(err.Error(), testToken) {
		t.Errorf("error leaks token: %v", err)
	}
}

func TestAuthHeaderSelection(t *testing.T) {
	tests := []struct {
		name       string
		cred       Credential
		wantHeader string
		wantValue  string
		absent     string
	}{
		{"account token", Credential{Token: testToken}, "Authorization", "Bearer " + testToken, "Project-Access-Token"},
		{"project token", Credential{ProjectToken: "project-token"}, "Project-Access-Token", "project-token", "Authorization"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFake(t, func(_ int, _ recordedRequest, w http.ResponseWriter) {
				writeJSON(w, 200, `{"data":{"ok":true}}`)
			})
			if err := f.client(t, tt.cred).Do(context.Background(), "query { ok }", nil, nil); err != nil {
				t.Fatal(err)
			}
			h := f.recorded()[0].Header
			if got := h.Get(tt.wantHeader); got != tt.wantValue {
				t.Errorf("%s = %q, want %q", tt.wantHeader, got, tt.wantValue)
			}
			if got := h.Get(tt.absent); got != "" {
				t.Errorf("%s leaked: %q", tt.absent, got)
			}
		})
	}
}

func TestRedirectIsRefused(t *testing.T) {
	target := newFake(t, func(_ int, _ recordedRequest, w http.ResponseWriter) {
		writeJSON(w, 200, `{"data":{"ok":true}}`)
	})
	redirector := newFake(t, func(_ int, _ recordedRequest, w http.ResponseWriter) {
		w.Header().Set("Location", target.server.URL)
		w.WriteHeader(http.StatusTemporaryRedirect)
	})
	err := redirector.client(t, Credential{Token: testToken}, WithHTTPClient(&http.Client{})).Do(context.Background(), "query { ok }", nil, nil)
	if err == nil {
		t.Fatal("redirect was followed")
	}
	if len(target.recorded()) != 0 {
		t.Error("request reached the redirect target")
	}
	if strings.Contains(err.Error(), testToken) {
		t.Errorf("error leaks token: %v", err)
	}
}

func TestGraphQLErrorsSurfaceWithoutToken(t *testing.T) {
	f := newFake(t, func(_ int, _ recordedRequest, w http.ResponseWriter) {
		writeJSON(w, 200, `{"data":null,"errors":[{"message":"Not Authorized","path":["environment","config"]}]}`)
	})
	err := f.client(t, Credential{Token: testToken}).Do(context.Background(), "query { environment }", nil, nil)
	var gqlErr *GraphQLError
	if !errors.As(err, &gqlErr) {
		t.Fatalf("err = %v, want *GraphQLError", err)
	}
	if gqlErr.Errors[0].Message != "Not Authorized" {
		t.Errorf("message = %q", gqlErr.Errors[0].Message)
	}
	if !strings.Contains(err.Error(), "environment.config") {
		t.Errorf("error omits path: %v", err)
	}
	if strings.Contains(err.Error(), testToken) {
		t.Errorf("error leaks token: %v", err)
	}
}

func TestGraphQLErrorsOnBadRequestStatus(t *testing.T) {
	f := newFake(t, func(_ int, _ recordedRequest, w http.ResponseWriter) {
		writeJSON(w, 400, `{"errors":[{"message":"Cannot query field \"x\""}]}`)
	})
	err := f.client(t, Credential{Token: testToken}).Do(context.Background(), "query { x }", nil, nil)
	var gqlErr *GraphQLError
	if !errors.As(err, &gqlErr) || gqlErr.StatusCode != 400 {
		t.Fatalf("err = %v, want *GraphQLError with status 400", err)
	}
	if len(f.recorded()) != 1 {
		t.Errorf("400 was retried: %d requests", len(f.recorded()))
	}
}

func TestRetriesServiceUnavailableThenSucceeds(t *testing.T) {
	f := newFake(t, func(n int, _ recordedRequest, w http.ResponseWriter) {
		if n < 2 {
			w.Header().Set("Retry-After", "0")
			writeJSON(w, 503, `unavailable`)
			return
		}
		writeJSON(w, 200, `{"data":{"value":42}}`)
	})
	var out struct {
		Value int `json:"value"`
	}
	if err := f.client(t, Credential{Token: testToken}).Do(context.Background(), "query { value }", nil, &out); err != nil {
		t.Fatal(err)
	}
	if out.Value != 42 || len(f.recorded()) != 3 {
		t.Errorf("value = %d after %d requests, want 42 after 3", out.Value, len(f.recorded()))
	}
}

func TestRetryGivesUpWithoutLeakingToken(t *testing.T) {
	f := newFake(t, func(_ int, _ recordedRequest, w http.ResponseWriter) {
		writeJSON(w, 502, `bad gateway`)
	})
	err := f.client(t, Credential{Token: testToken}).Do(context.Background(), "query { value }", nil, nil)
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != 502 {
		t.Fatalf("err = %v, want *HTTPError 502", err)
	}
	if got := len(f.recorded()); got != maxAttempts {
		t.Errorf("requests = %d, want %d", got, maxAttempts)
	}
	if strings.Contains(err.Error(), testToken) {
		t.Errorf("error leaks token: %v", err)
	}
}

func TestRetryStopsWhenContextEnds(t *testing.T) {
	f := newFake(t, func(_ int, _ recordedRequest, w http.ResponseWriter) {
		w.Header().Set("Retry-After", "60")
		writeJSON(w, 429, `slow down`)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := f.client(t, Credential{Token: testToken}).Do(ctx, "query { value }", nil, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want deadline exceeded", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Error("Retry-After wait ignored the context")
	}
}

func TestRetryAfterParsing(t *testing.T) {
	if d, ok := retryAfter("3"); !ok || d != 3*time.Second {
		t.Errorf("retryAfter(3) = %v, %v", d, ok)
	}
	future := time.Now().Add(10 * time.Second).UTC().Format(http.TimeFormat)
	if d, ok := retryAfter(future); !ok || d <= 0 || d > 11*time.Second {
		t.Errorf("retryAfter(date) = %v, %v", d, ok)
	}
	if _, ok := retryAfter("soon"); ok {
		t.Error("retryAfter accepted garbage")
	}
}
