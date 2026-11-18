package transport

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

type stubRoundTripper struct{}

func (stubRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, nil
}

func TestNewPreservesDefaultOrInjectedProxyPolicy(t *testing.T) {
	created := New("https://example.test/api/v1", time.Second, time.Second, nil, "sdk")
	createdTransport, ok := created.httpClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("created transport = %T, want *http.Transport", created.httpClient.Transport)
	}
	if createdTransport.Proxy == nil {
		t.Fatal("created transport lost Go's default environment proxy resolver")
	}

	injectedTransport := &http.Transport{Proxy: nil}
	injected := &http.Client{Transport: injectedTransport}
	configured := New("https://example.test/api/v1", time.Second, time.Second, injected, "sdk")
	if configured.httpClient != injected {
		t.Fatal("New mutated or replaced the injected HTTP client")
	}
	if configured.httpClient.Transport != injectedTransport {
		t.Fatal("New changed the injected proxy/transport policy")
	}

	originalDefault := http.DefaultTransport
	http.DefaultTransport = stubRoundTripper{}
	t.Cleanup(func() { http.DefaultTransport = originalDefault })
	fallback := New("https://example.test/api/v1", time.Second, time.Second, nil, "sdk")
	fallbackTransport, ok := fallback.httpClient.Transport.(*http.Transport)
	if !ok || fallbackTransport.Proxy == nil {
		t.Fatal("fallback transport did not restore Go's default proxy resolver")
	}
}

func TestAccessChannelHeaderOnEveryMethod(t *testing.T) {
	const want = "myapp"
	var saw []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		saw = append(saw, r.Method+" "+r.URL.Path+"="+r.Header.Get("X-OP-ACCESS-CHANNEL"))
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"code":"ok","data":{}}`)
	}))
	defer server.Close()

	client := New(server.URL, time.Second, time.Second, nil, want)
	ctx := context.Background()

	if _, err := client.Get(ctx, "/get", url.Values{"a": {"b"}}, "tok"); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if _, err := client.PostJSON(ctx, "/post", map[string]string{"k": "v"}, "tok"); err != nil {
		t.Fatalf("PostJSON: %v", err)
	}
	if _, err := client.PostJSONWithHeaders(ctx, "/posth", map[string]string{"k": "v"}, "tok", nil); err != nil {
		t.Fatalf("PostJSONWithHeaders: %v", err)
	}
	if _, err := client.PutJSONWithHeaders(ctx, "/put", map[string]string{"k": "v"}, "tok", nil); err != nil {
		t.Fatalf("PutJSONWithHeaders: %v", err)
	}
	if _, err := client.DeleteWithHeaders(ctx, "/delete", url.Values{"a": {"b"}}, "tok", nil); err != nil {
		t.Fatalf("DeleteWithHeaders: %v", err)
	}
	if _, err := client.PostForm(ctx, "/form", url.Values{"a": {"b"}}, "tok"); err != nil {
		t.Fatalf("PostForm: %v", err)
	}

	if len(saw) != 6 {
		t.Fatalf("captured %d requests, want 6: %v", len(saw), saw)
	}
	suffix := "=" + want
	for _, entry := range saw {
		if !strings.HasSuffix(entry, suffix) {
			t.Errorf("request %q missing X-OP-ACCESS-CHANNEL %q", entry, want)
		}
	}
}
