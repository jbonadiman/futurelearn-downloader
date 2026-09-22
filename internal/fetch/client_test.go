package fetch

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeCookieFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cookies.txt")
	content := "# Netscape HTTP Cookie File\n" +
		".futurelearn.com\tTRUE\t/\tTRUE\t0\tcf_clearance\tabc123\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write cookie file: %v", err)
	}
	return path
}

func TestClientRotatesProfileOn403(t *testing.T) {
	var seen int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen++
		if seen == 1 {
			w.WriteHeader(http.StatusForbidden) // "Just a moment..."
			return
		}
		fmt.Fprint(w, "<html>ok</html>")
	}))
	defer srv.Close()

	c, err := NewClient(Options{CookiesPath: writeCookieFile(t), Delay: 0})
	if err != nil {
		t.Fatal(err)
	}
	body, err := c.Text(srv.URL)
	if err != nil {
		t.Fatalf("Text: %v", err)
	}
	if !strings.Contains(body, "ok") {
		t.Fatalf("body = %q", body)
	}
	if seen != 2 {
		t.Fatalf("requests = %d, want 2 (one 403 then a rotated retry)", seen)
	}
}

func TestClientGivesUpAfterThreeProfiles(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	c, _ := NewClient(Options{CookiesPath: writeCookieFile(t), Delay: 0, Log: io.Discard})
	_, err := c.Text(srv.URL)
	if err == nil || !strings.Contains(err.Error(), "cookies.txt") {
		t.Fatalf("want a re-export hint, got %v", err)
	}
}

// A non-200 must fail the same way through every body-fetching method:
// one rule, one message, owned by Client.get. Without this the rule can
// drift apart between Text, Bytes and ToFile again.
func TestNon200IsOneErrorForEveryFetch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	c, err := NewClient(Options{CookiesPath: writeCookieFile(t), Delay: 0, Log: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	calls := map[string]func() error{
		"Text":   func() error { _, err := c.Text(srv.URL); return err },
		"Bytes":  func() error { _, err := c.Bytes(srv.URL); return err },
		"ToFile": func() error { _, err := c.ToFile(srv.URL, filepath.Join(t.TempDir(), "f")); return err },
	}
	for name, call := range calls {
		err := call()
		if err == nil || !strings.Contains(err.Error(), "status 404") {
			t.Errorf("%s: err = %v, want a %q error", name, err, "status 404")
		}
	}
}

// A request that carries no browser headers is challenged by Cloudflare no
// matter which TLS profile sends it, so the header set is as load-bearing as
// the fingerprint. This fails if the headers stop being applied.
func TestClientSendsBrowserHeaders(t *testing.T) {
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		fmt.Fprint(w, "ok")
	}))
	defer srv.Close()

	c, err := NewClient(Options{CookiesPath: writeCookieFile(t), Delay: 0, Log: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Text(srv.URL); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{
		"User-Agent", "Accept", "Accept-Language", "Accept-Encoding",
		"sec-ch-ua", "sec-ch-ua-mobile", "sec-ch-ua-platform",
		"sec-fetch-site", "sec-fetch-mode", "sec-fetch-user", "sec-fetch-dest",
		"upgrade-insecure-requests", "priority",
	} {
		if got.Get(name) == "" {
			t.Errorf("request is missing the %s header", name)
		}
	}
	if ua := got.Get("User-Agent"); !strings.HasPrefix(ua, "Mozilla/5.0") {
		t.Errorf("User-Agent = %q, want a browser user agent", ua)
	}
}
