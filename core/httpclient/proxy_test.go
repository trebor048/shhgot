package httpclient

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	xnetproxy "golang.org/x/net/proxy"
)

// stubContextDialer satisfies xnetproxy.ContextDialer so the SOCKS selection
// test never opens a socket.
type stubContextDialer struct{}

func (stubContextDialer) DialContext(context.Context, string, string) (net.Conn, error) {
	return nil, errors.New("stubContextDialer: refusing to dial")
}

func TestHTTPProxyHandlesRequest(t *testing.T) {
	var hits int32
	var target atomic.Value
	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		target.Store(r.URL.String())
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "via-proxy")
	}))
	defer proxySrv.Close()

	clock := newFakeClock()
	c := newTestClient(t, clock, Config{ProxyURL: proxySrv.URL, Timeout: time.Second})

	// A plain http target is sent to an http proxy in absolute form; the proxy
	// answers directly, so no real target is contacted.
	req, err := http.NewRequest(http.MethodGet, "http://example.com/thing", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()

	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("proxy received %d requests, want 1", got)
	}
	if got, _ := target.Load().(string); got != "http://example.com/thing" {
		t.Fatalf("proxy saw target %q, want the absolute request URI", got)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if string(body) != "via-proxy" {
		t.Fatalf("body = %q, want %q", body, "via-proxy")
	}
}

func TestSocksProxySelectsSocksDialer(t *testing.T) {
	for _, scheme := range []string{"socks5", "socks5h"} {
		t.Run(scheme, func(t *testing.T) {
			var calls int32
			var seenScheme atomic.Value

			original := newSocksDialer
			newSocksDialer = func(u *url.URL) (xnetproxy.ContextDialer, error) {
				atomic.AddInt32(&calls, 1)
				seenScheme.Store(u.Scheme)
				return stubContextDialer{}, nil
			}
			t.Cleanup(func() { newSocksDialer = original })

			clock := newFakeClock()
			c := newTestClient(t, clock, Config{
				ProxyURL: scheme + "://127.0.0.1:1080",
				Timeout:  time.Second,
			})

			tr, ok := c.http.Transport.(*http.Transport)
			if !ok {
				t.Fatalf("transport is %T, want *http.Transport", c.http.Transport)
			}
			if got := atomic.LoadInt32(&calls); got != 1 {
				t.Fatalf("socks dialer factory calls = %d, want 1", got)
			}
			if got, _ := seenScheme.Load().(string); got != scheme {
				t.Fatalf("factory saw scheme %q, want %q", got, scheme)
			}
			if tr.Proxy != nil {
				t.Fatal("transport Proxy is set for a socks proxy; the dialer must own proxying")
			}
			if tr.DialContext == nil {
				t.Fatal("transport DialContext is nil for a socks proxy")
			}
			if c.pool != nil {
				t.Fatal("single-URL config produced a rotating pool")
			}
		})
	}
}

func TestNoProxyDialsDirect(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	clock := newFakeClock()
	c := newTestClient(t, clock, Config{Timeout: time.Second})

	tr, ok := c.http.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport is %T, want *http.Transport", c.http.Transport)
	}
	if tr.DialContext != nil {
		t.Fatal("DialContext is set without a proxy; want the default direct dialer")
	}
	if tr.Proxy == nil {
		t.Fatal("Proxy is nil without a proxy; want environment settings kept")
	}
	if c.pool != nil {
		t.Fatal("no-proxy config produced a rotating pool")
	}

	req, err := http.NewRequest(http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("target received %d requests, want 1", got)
	}
}
