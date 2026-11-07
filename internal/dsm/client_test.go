package dsm

import (
	"context"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func testClient(t *testing.T, handler http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := New(server.URL, "fixture-user", "fixture-password", "", ca)
	if err != nil {
		t.Fatal(err)
	}
	return c, server
}

func TestVerifiedHTTPS(t *testing.T) {
	for _, endpoint := range []string{"http://nas.test", "https://user:password@nas.test", "https://nas.test/path", "https://nas.test?password=secret"} {
		if _, err := New(endpoint, "u", "p", "", ""); err == nil {
			t.Fatalf("accepted invalid endpoint %q", endpoint)
		}
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"success":true,"data":{}}`) }))
	defer server.Close()
	c, err := New(server.URL, "u", "p", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Connect(context.Background()); err == nil {
		t.Fatal("accepted an untrusted TLS certificate")
	}
}

func TestRequestRedactionAndNoRetries(t *testing.T) {
	var requests atomic.Int64
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != "POST" || r.URL.RawQuery != "" {
			t.Error("credentials must only be sent in a POST body")
		}
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		switch r.Form.Get("api") {
		case "SYNO.API.Info":
			fmt.Fprint(w, `{"success":true,"data":{"SYNO.API.Auth":{"path":"entry.cgi","minVersion":1,"maxVersion":7},"SYNO.Core.Share":{"path":"entry.cgi","minVersion":1,"maxVersion":1}}}`)
		case "SYNO.API.Auth":
			if r.Form.Get("passwd") != "fixture-password" {
				t.Error("password was not passed correctly")
			}
			fmt.Fprint(w, `{"success":true,"data":{"sid":"fixture-session","synotoken":"fixture-token"}}`)
		default:
			if r.Form.Get("_sid") != "fixture-session" || r.Header.Get("X-SYNO-TOKEN") != "fixture-token" {
				t.Error("session authentication missing")
			}
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, "fixture-password fixture-session fixture-token")
		}
	})
	if err := c.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	err := c.WriteShare(context.Background(), "create", Share{Name: "fixture", Volume: "/volume1"})
	if err == nil {
		t.Fatal("expected failure")
	}
	for _, secret := range []string{"fixture-password", "fixture-session", "fixture-token"} {
		if strings.Contains(err.Error(), secret) || strings.Contains(fmt.Sprintf("%+v %#v", c, c), secret) {
			t.Fatal("secret leaked")
		}
	}
	if requests.Load() != 3 {
		t.Fatalf("unexpected automatic mutation retry: %d requests", requests.Load())
	}
}

func TestRedirectNeverForwardsCredentials(t *testing.T) {
	var hits atomic.Int64
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer destination.Close()
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	})
	if err := c.Connect(context.Background()); err == nil {
		t.Fatal("accepted redirect")
	}
	if hits.Load() != 0 {
		t.Fatal("forwarded credentials to redirect target")
	}
}

func TestMalformedListingDoesNotEstablishAbsence(t *testing.T) {
	for _, body := range []string{`{"success":true,"data":{}}`, `{"success":true,"data":{"shares":[],"total":1}}`, `{"success":false,"error":{"code":105}}`, `<html>fixture-secret</html>`} {
		t.Run(body, func(t *testing.T) {
			c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) })
			c.apis = map[string]Capability{"SYNO.Core.Share": {Path: "entry.cgi", Min: 1, Max: 1}}
			_, err := c.Share(context.Background(), "missing")
			if err == nil || errors.Is(err, ErrNotFound) {
				t.Fatalf("failure was treated as absence: %v", err)
			}
			if strings.Contains(err.Error(), "fixture-secret") {
				t.Fatal("response leaked")
			}
		})
	}
}

func TestPermissionSemantics(t *testing.T) {
	for _, tc := range []struct {
		p    Permission
		want string
	}{{Permission{}, "inherit"}, {Permission{Deny: true}, "deny"}, {Permission{Writable: true}, "read_write"}, {Permission{ReadOnly: true}, "read_only"}} {
		got, err := tc.p.Access()
		if err != nil || got != tc.want {
			t.Fatalf("got %q, %v; want %q", got, err, tc.want)
		}
	}
	if _, err := (Permission{Deny: true, Writable: true}).Access(); err == nil {
		t.Fatal("accepted conflicting permission flags")
	}
}
