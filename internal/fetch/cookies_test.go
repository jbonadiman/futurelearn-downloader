package fetch

import (
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

// TestLoadNetscapeCookiesHonoursSecureFlag pins that a cookie the file marks
// Secure is not handed to a plain-http request. Netscape's column 4 is the
// flag; ignoring it lets a session cookie leave over cleartext when a course
// page points the scraper at an http:// asset URL.
func TestLoadNetscapeCookiesHonoursSecureFlag(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cookies.txt")
	content := "# Netscape HTTP Cookie File\n" +
		".futurelearn.com\tTRUE\t/\tTRUE\t0\tsessionid\tsecret\n" +
		".futurelearn.com\tTRUE\t/\tFALSE\t0\tcsrftoken\ttok\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	jar, err := loadNetscapeCookies(path)
	if err != nil {
		t.Fatal(err)
	}

	names := func(raw string) map[string]bool {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]bool{}
		for _, c := range jar.Cookies(u) {
			out[c.Name] = true
		}
		return out
	}

	https := names("https://www.futurelearn.com/")
	if !https["sessionid"] || !https["csrftoken"] {
		t.Fatalf("https request should carry both cookies, got %v", https)
	}
	http := names("http://www.futurelearn.com/")
	if http["sessionid"] {
		t.Fatalf("Secure sessionid must not be sent over http, got %v", http)
	}
	if !http["csrftoken"] {
		t.Fatalf("non-Secure csrftoken should still be sent over http, got %v", http)
	}
}
