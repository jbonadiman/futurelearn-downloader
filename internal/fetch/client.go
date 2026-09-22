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
	"sync"
	"time"

	fhttp "github.com/bogdanfinn/fhttp"
	fhttpcookiejar "github.com/bogdanfinn/fhttp/cookiejar"
	tls_client "github.com/bogdanfinn/tls-client"
	"github.com/bogdanfinn/tls-client/profiles"
)

// chromeProfiles is the Cloudflare-rotation order: bounded to 3 attempts,
// spaced by Options.Delay. Kept in one place because a library update can
// resequence which Chrome version profile.go maps to which name. Version is
// the Chrome release the profile stands for, and drives the matching headers.
var chromeProfiles = []struct {
	Name    string
	Version string
	Profile profiles.ClientProfile
}{
	{"Chrome_152", "152", profiles.Chrome_152},
	{"Chrome_144", "144", profiles.Chrome_144},
	{"Chrome_133", "133", profiles.Chrome_133},
}

// browserHeaders is the header set a real Chrome sends on a top-level
// navigation. Impersonating the TLS fingerprint alone is not enough: a request
// carrying no User-Agent or sec-fetch-* headers is challenged regardless of
// which TLS profile sends it, so these travel with every request. Cloudflare
// fingerprints the order too, so it is declared rather than left to the
// header map's iteration order.
func browserHeaders(version string) fhttp.Header {
	return fhttp.Header{
		"sec-ch-ua":                 {`"Chromium";v="` + version + `", "Google Chrome";v="` + version + `", "Not-A.Brand";v="99"`},
		"sec-ch-ua-mobile":          {"?0"},
		"sec-ch-ua-platform":        {`"macOS"`},
		"upgrade-insecure-requests": {"1"},
		"user-agent":                {"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/" + version + ".0.0.0 Safari/537.36"},
		"accept":                    {"text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.7"},
		"sec-fetch-site":            {"none"},
		"sec-fetch-mode":            {"navigate"},
		"sec-fetch-user":            {"?1"},
		"sec-fetch-dest":            {"document"},
		"accept-encoding":           {"gzip, deflate, br, zstd"},
		"accept-language":           {"en-US,en;q=0.9"},
		"priority":                  {"u=0, i"},
		fhttp.HeaderOrderKey: {
			"sec-ch-ua", "sec-ch-ua-mobile", "sec-ch-ua-platform", "upgrade-insecure-requests",
			"user-agent", "accept", "sec-fetch-site", "sec-fetch-mode", "sec-fetch-user",
			"sec-fetch-dest", "accept-encoding", "accept-language", "priority",
		},
		fhttp.PHeaderOrderKey: {":method", ":authority", ":scheme", ":path"},
	}
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

	// mu guards clients, and clients holds one reusable tls-client per
	// chromeProfiles entry, built on first use.
	mu      sync.Mutex
	clients []tls_client.HttpClient
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
	return &Client{
		jar:     jar,
		delay:   opts.Delay,
		log:     log,
		clients: make([]tls_client.HttpClient, len(chromeProfiles)),
	}, nil
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

// httpClient returns the client for chromeProfiles[i], building it on
// first use. One client per profile lives for the whole run, so its
// connection pool is reused and consecutive fetches to the same host
// skip the TCP/TLS handshake.
func (c *Client) httpClient(i int) (tls_client.HttpClient, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.clients[i] != nil {
		return c.clients[i], nil
	}
	cp := chromeProfiles[i]
	client, err := tls_client.NewHttpClient(tls_client.NewNoopLogger(),
		tls_client.WithClientProfile(cp.Profile),
		tls_client.WithDefaultHeaders(browserHeaders(cp.Version)),
		tls_client.WithCookieJar(c.jar),
		tls_client.WithTimeoutSeconds(60),
		tls_client.WithRandomTLSExtensionOrder(),
	)
	if err != nil {
		return nil, err
	}
	c.clients[i] = client
	return client, nil
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
		client, err := c.httpClient(i)
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

// get sends one GET and returns the response once its status is 200,
// leaving the body open for the caller to close. Any other status is an
// error naming the target — the one place that rule lives.
func (c *Client) get(target string) (*fhttp.Response, error) {
	resp, err := c.do(fhttp.MethodGet, target)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != fhttp.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("%s: status %d", target, resp.StatusCode)
	}
	return resp, nil
}

// Text fetches target and returns its body as a string.
func (c *Client) Text(target string) (string, error) {
	resp, err := c.get(target)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// Bytes fetches the target and returns its full body.
func (c *Client) Bytes(target string) ([]byte, error) {
	text, err := c.Text(target)
	if err != nil {
		return nil, err
	}
	return []byte(text), nil
}

// ToFile streams target's response body straight to dest, returning the
// number of bytes written.
func (c *Client) ToFile(target, dest string) (int64, error) {
	resp, err := c.get(target)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
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
