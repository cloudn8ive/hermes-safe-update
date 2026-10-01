package testutil

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGuardDialRefusesRemoteHosts(t *testing.T) {
	g := newGuard()
	for _, addr := range []string{"20.29.134.17:443", "api.github.com:443", "192.168.1.5:80", "[2606:4700::1111]:443"} {
		_, err := g.DialContext(context.Background(), "tcp", addr)
		if err == nil || !strings.Contains(err.Error(), "network blocked in tests") {
			t.Errorf("dial %s: want guard error, got %v", addr, err)
		}
	}
	if got := g.Violations(); len(got) != 4 {
		t.Errorf("violations = %v, want 4", got)
	}
}

func TestGuardAllowsLoopback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	g := newGuard()
	c := &http.Client{Transport: g.Transport()}
	resp, err := c.Get(srv.URL)
	if err != nil {
		t.Fatalf("loopback get: %v", err)
	}
	resp.Body.Close()
	if v := g.Violations(); len(v) != 0 {
		t.Errorf("violations = %v", v)
	}
}

func TestGuardTransportRefusesRemoteURL(t *testing.T) {
	g := newGuard()
	c := &http.Client{Transport: g.Transport()}
	_, err := c.Get("https://api.github.com/repos/x/y")
	if err == nil || !strings.Contains(err.Error(), "network blocked in tests") {
		t.Fatalf("want guard error, got %v", err)
	}
	if len(g.Violations()) != 1 {
		t.Errorf("violations = %v", g.Violations())
	}
}

func TestGuardResolverRefusesDNS(t *testing.T) {
	g := newGuard()
	r := g.Resolver()
	if _, err := r.LookupHost(context.Background(), "api.github.com"); err == nil {
		t.Fatal("DNS lookup should fail")
	}
	if len(g.Violations()) == 0 {
		t.Error("DNS attempt not recorded")
	}
	_ = net.IPv4zero
}

func TestNoNetworkInstallsGlobalGuard(t *testing.T) {
	NoNetwork(t)
	_, err := http.Get("https://api.github.com/")
	if err == nil || !strings.Contains(err.Error(), "network blocked in tests") {
		t.Fatalf("default client not guarded: %v", err)
	}
	// Remove the recorded violation so the cleanup check passes: this test
	// provoked it on purpose.
	ClearViolations()
}
