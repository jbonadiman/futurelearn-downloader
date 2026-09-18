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
