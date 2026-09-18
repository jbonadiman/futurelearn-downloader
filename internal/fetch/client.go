// Package fetch is a TLS-impersonating HTTP client. FutureLearn sits behind
// Cloudflare bot management: a browser's cf_clearance cookie is bound to
// that browser's TLS/JA3 fingerprint, so a plain Go net/http client is
// 403-challenged even with valid cookies. tls-client impersonates a real
// Chrome fingerprint so the same cookies.txt exported from a browser is
// accepted directly.
package fetch

import (
	"bufio"
	"fmt"
	"io"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	fhttp "github.com/bogdanfinn/fhttp"
	fhttpcookiejar "github.com/bogdanfinn/fhttp/cookiejar"
	tls_client "github.com/bogdanfinn/tls-client"
	"github.com/bogdanfinn/tls-client/profiles"
)

// chromeProfiles is the Cloudflare-rotation order: bounded to 3 attempts,
// spaced by Options.Delay. Kept in one place because a library update can
// resequence which Chrome version profile.go maps to which name.
var chromeProfiles = []struct {
	Name    string
	Profile profiles.ClientProfile
}{
	{"Chrome_152", profiles.Chrome_152},
	{"Chrome_144", profiles.Chrome_144},
	{"Chrome_133", profiles.Chrome_133},
}

// Options configures a Client.
type Options struct {
	CookiesPath string
	Delay       time.Duration
	Log         io.Writer
}

// Client is a Cloudflare-rotating, cookie-authenticated HTTP client.
type Client struct {
	jar   *fhttpcookiejar.Jar
	delay time.Duration
	log   io.Writer
}

// NewClient loads cookies.txt into a fresh jar and returns a Client ready to
// fetch. The underlying tls-client HttpClient is built fresh per attempt (see
// do) since each Cloudflare-rotation retry needs a different client profile.
func NewClient(opts Options) (*Client, error) {
	jar, err := loadNetscapeCookies(opts.CookiesPath)
	if err != nil {
		return nil, err
	}
	log := opts.Log
	if log == nil {
		log = os.Stderr
	}
	return &Client{jar: jar, delay: opts.Delay, log: log}, nil
}

// loadNetscapeCookies parses a Netscape-format cookies.txt into an fhttp
// cookie jar.
func loadNetscapeCookies(path string) (*fhttpcookiejar.Jar, error) {
	jar, err := fhttpcookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "#HttpOnly_") {
			line = strings.TrimPrefix(line, "#HttpOnly_")
		} else if strings.HasPrefix(line, "#") || strings.TrimSpace(line) == "" {
			continue
		}
		p := strings.Split(line, "\t")
		if len(p) < 7 {
			continue
		}
		u, err := url.Parse("https://" + strings.TrimPrefix(p[0], "."))
		if err != nil {
			continue
		}
		var expires time.Time
		if exp, err := strconv.ParseInt(p[4], 10, 64); err == nil && exp > 0 {
			expires = time.Unix(exp, 0)
		}
		jar.SetCookies(u, []*fhttp.Cookie{{
			Name:    p[5],
			Value:   p[6],
			Path:    p[2],
			Domain:  p[0],
			Expires: expires,
		}})
	}
	return jar, sc.Err()
}

// Pace sleeps Options.Delay between successive fetches.
func (c *Client) Pace() {
	if c.delay > 0 {
		time.Sleep(c.delay)
	}
}

// do sends one request, rotating through chromeProfiles on a 403 (the
// Cloudflare challenge response) until one succeeds or the list is
// exhausted. Each attempt after the first is paced by Options.Delay.
func (c *Client) do(method, target string) (*fhttp.Response, error) {
	var lastErr error
	for i, cp := range chromeProfiles {
		if i > 0 {
			c.Pace()
			fmt.Fprintf(c.log, "  (403 from %s; retrying as %s)\n", chromeProfiles[i-1].Name, cp.Name)
		}
		client, err := tls_client.NewHttpClient(tls_client.NewNoopLogger(),
			tls_client.WithClientProfile(cp.Profile),
			tls_client.WithCookieJar(c.jar),
			tls_client.WithTimeoutSeconds(60),
			tls_client.WithRandomTLSExtensionOrder(),
		)
		if err != nil {
			return nil, err
		}

		req, err := fhttp.NewRequest(method, target, nil)
		if err != nil {
			return nil, err
		}
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode == fhttp.StatusForbidden {
			resp.Body.Close()
			lastErr = fmt.Errorf("status %d", resp.StatusCode)
			continue
		}
		return resp, nil
	}
	return nil, fmt.Errorf("%s: Cloudflare kept rejecting every client profile (%v) — re-export cookies.txt from your browser and try again", target, lastErr)
}

// Text fetches target and returns its body as a string.
func (c *Client) Text(target string) (string, error) {
	resp, err := c.do(fhttp.MethodGet, target)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != fhttp.StatusOK {
		return "", fmt.Errorf("%s: status %d", target, resp.StatusCode)
	}
	return string(b), nil
}

// Bytes fetches the target and returns its full body.
func (c *Client) Bytes(target string) ([]byte, error) {
	resp, err := c.do(fhttp.MethodGet, target)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != fhttp.StatusOK {
		return nil, fmt.Errorf("%s: status %d", target, resp.StatusCode)
	}
	return b, nil
}

// ToFile streams target's response body straight to dest, returning the
// number of bytes written.
func (c *Client) ToFile(target, dest string) (int64, error) {
	resp, err := c.do(fhttp.MethodGet, target)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != fhttp.StatusOK {
		return 0, fmt.Errorf("%s: status %d", target, resp.StatusCode)
	}
	return writeResponseBody(resp, dest)
}

func writeResponseBody(resp *fhttp.Response, dest string) (int64, error) {
	f, err := os.Create(dest)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	return io.Copy(f, resp.Body)
}

// FinalURL fetches target and returns the URL it redirected to. A
// redirect-shortener link that's briefly down shouldn't block writing the
// rest of the page, so — unlike Text/Bytes/ToFile — any failure (network
// error, non-2xx status) falls back to returning target unchanged instead
// of erroring.
func (c *Client) FinalURL(target string) string {
	resp, err := c.do(fhttp.MethodGet, target)
	if err != nil {
		return target
	}
	defer resp.Body.Close()
	if resp.Request != nil && resp.Request.URL != nil {
		return resp.Request.URL.String()
	}
	return target
}
