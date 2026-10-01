package testutil

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
)

// The test suite must never reach the network: the user's firewall prompts
// for every new program and Go test binaries get a new path on every build.
// Every package calls Main from its TestMain; that swaps the process-wide
// HTTP transport, dialer and DNS resolver for ones that refuse anything but
// loopback and record each attempt. A refused attempt is an error to the
// caller and also fails the test binary at exit (so code that swallows the
// error still goes red).

const blockedMsg = "network blocked in tests"

type guard struct {
	mu   sync.Mutex
	hits []string
}

func newGuard() *guard { return &guard{} }

func (g *guard) record(what string) error {
	g.mu.Lock()
	g.hits = append(g.hits, what)
	g.mu.Unlock()
	return fmt.Errorf("%s: refused outbound %s (use httptest or a fake; real-network tests need -tags network)", blockedMsg, what)
}

// Violations lists the refused attempts so far.
func (g *guard) Violations() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.hits...)
}

func (g *guard) clear() {
	g.mu.Lock()
	g.hits = nil
	g.mu.Unlock()
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	if i := strings.IndexByte(host, '%'); i >= 0 { // zone
		host = host[:i]
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (g *guard) checkAddr(network, addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	if network == "unix" || network == "unixgram" || network == "unixpacket" || isLoopbackHost(host) {
		return nil
	}
	return g.record(network + " " + addr)
}

// DialContext refuses every non-loopback address.
func (g *guard) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	if err := g.checkAddr(network, addr); err != nil {
		return nil, err
	}
	var d net.Dialer
	return d.DialContext(ctx, network, addr)
}

// Resolver never talks to a DNS server.
func (g *guard) Resolver() *net.Resolver {
	return &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
		return nil, g.record("dns lookup via " + addr)
	}}
}

type guardTransport struct {
	g    *guard
	base *http.Transport
}

func (t guardTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if err := t.g.checkAddr("tcp", r.URL.Host); err != nil {
		return nil, err
	}
	return t.base.RoundTrip(r)
}

// Transport is an HTTP transport that only reaches loopback (proxy env is
// ignored so the request host itself is what gets checked).
func (g *guard) Transport() http.RoundTripper {
	base := &http.Transport{DialContext: g.DialContext, ForceAttemptHTTP2: true}
	return guardTransport{g: g, base: base}
}

var global = newGuard()

// Install swaps the process-wide defaults for guarded ones.
func Install() {
	http.DefaultTransport = global.Transport()
	net.DefaultResolver = global.Resolver()
}

// Main is the body of every package's TestMain:
//
//	func TestMain(m *testing.M) { testutil.Main(m) }
func Main(m *testing.M) {
	Install()
	code := m.Run()
	if v := global.Violations(); len(v) > 0 {
		fmt.Fprintf(os.Stderr, "\nFAIL: %s: %d outbound attempt(s):\n  %s\n", blockedMsg, len(v), strings.Join(v, "\n  "))
		if code == 0 {
			code = 1
		}
	}
	os.Exit(code)
}

// NoNetwork guards one test: it installs the guard (idempotent) and fails t
// at cleanup if anything dialled out during it.
func NoNetwork(t testing.TB) {
	t.Helper()
	Install()
	before := len(global.Violations())
	t.Cleanup(func() {
		if v := global.Violations(); len(v) > before {
			t.Errorf("%s: %s", blockedMsg, strings.Join(v[before:], "; "))
		}
	})
}

// ClearViolations forgets recorded attempts; for the guard's own tests that
// provoke one on purpose.
func ClearViolations() { global.clear() }
