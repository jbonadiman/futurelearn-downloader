package fetch

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	fhttp "github.com/bogdanfinn/fhttp"
	fhttpcookiejar "github.com/bogdanfinn/fhttp/cookiejar"
	tls_client "github.com/bogdanfinn/tls-client"
	"github.com/bogdanfinn/tls-client/profiles"
)

// hlsWorkers is the measured sweet spot for parallel segment downloads on
// the CDN (12 ~equal, 16 worse).
const hlsWorkers = 8

// hlsProfile is a plain, cookieless, fixed Chrome impersonation profile:
// vzaar's HLS media URLs carry their own signed context/token and don't
// need FutureLearn's Cloudflare clearance, so this is deliberately not the
// page-fetching Client's rotating chromeProfiles.
var hlsProfile = profiles.Chrome_124

func newHLSHTTPClient() (tls_client.HttpClient, error) {
	jar, err := fhttpcookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	return tls_client.NewHttpClient(tls_client.NewNoopLogger(),
		tls_client.WithClientProfile(hlsProfile),
		tls_client.WithCookieJar(jar),
		tls_client.WithTimeoutSeconds(120),
		tls_client.WithRandomTLSExtensionOrder(),
	)
}

var (
	bandwidthRE = regexp.MustCompile(`BANDWIDTH=(\d+)`)
	extinfRE    = regexp.MustCompile(`#EXTINF:([0-9.]+)`)
)

// pickVariant returns the highest-bandwidth non-I-FRAME variant URI from
// a master playlist.
func pickVariant(master string) (bandwidth int, uri string, ok bool) {
	lines := strings.Split(strings.ReplaceAll(master, "\r\n", "\n"), "\n")
	best := -1
	for i, line := range lines {
		if !strings.HasPrefix(line, "#EXT-X-STREAM-INF") || strings.Contains(line, "I-FRAME") {
			continue
		}
		m := bandwidthRE.FindStringSubmatch(line)
		if m == nil || i+1 >= len(lines) {
			continue
		}
		bw, err := strconv.Atoi(m[1])
		if err != nil || bw <= best {
			continue
		}
		best, bandwidth, uri, ok = bw, bw, lines[i+1], true
	}
	return
}

// parsePlaylist validates and parses a variant playlist: reject encrypted
// or live playlists, return segment URIs (still relative to the
// playlist's own URL) and total duration (sum of #EXTINF values).
func parsePlaylist(text string) (segments []string, duration float64, err error) {
	if strings.Contains(text, "#EXT-X-KEY") {
		return nil, 0, errors.New("encrypted segments (EXT-X-KEY) — ffmpeg handles these")
	}
	if !strings.Contains(text, "#EXT-X-ENDLIST") {
		return nil, 0, errors.New("live playlist (no ENDLIST) — ffmpeg handles these")
	}
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		if line != "" && !strings.HasPrefix(line, "#") {
			segments = append(segments, line)
		}
	}
	if len(segments) == 0 {
		return nil, 0, errors.New("segment list is empty")
	}
	for _, m := range extinfRE.FindAllStringSubmatch(text, -1) {
		f, _ := strconv.ParseFloat(m[1], 64)
		duration += f
	}
	return segments, duration, nil
}

// hlsGet fetches url with client and returns its body text alongside the
// final (post-redirect) URL, so child/segment URIs resolve correctly.
func hlsGet(client tls_client.HttpClient, target string) (text, final string, err error) {
	req, err := fhttp.NewRequest(fhttp.MethodGet, target, nil)
	if err != nil {
		return "", "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", err
	}
	if resp.StatusCode != fhttp.StatusOK {
		return "", "", fmt.Errorf("%s: status %d", target, resp.StatusCode)
	}
	final = target
	if resp.Request != nil && resp.Request.URL != nil {
		final = resp.Request.URL.String()
	}
	return string(b), final, nil
}

func resolveURL(base, ref string) (string, error) {
	b, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	r, err := b.Parse(ref)
	if err != nil {
		return "", err
	}
	return r.String(), nil
}

// downloadSegment streams url to dest in 64 KiB chunks and returns the
// byte count written.
func downloadSegment(client tls_client.HttpClient, target, dest string) (int64, error) {
	req, err := fhttp.NewRequest(fhttp.MethodGet, target, nil)
	if err != nil {
		return 0, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != fhttp.StatusOK {
		return 0, fmt.Errorf("%s: status %d", target, resp.StatusCode)
	}
	f, err := os.Create(dest)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	return io.CopyBuffer(f, resp.Body, make([]byte, 1<<16))
}

// runMux drives one ffmpeg mpegts-over-stdin pass: feed writes segment bytes
// to ffmpeg's stdin (in playlist order) and closes it on completion; stderr
// is drained on its own goroutine so -loglevel error output can never fill
// the pipe; ffmpeg is bounded at 600s; a successful mux is backstopped by an
// ffprobe duration check before the final rename. Shared by muxSegments
// (already-downloaded segments) and downloadHLSNative (segments arriving
// concurrently, fed as each becomes ready).
func runMux(feed func(stdin io.WriteCloser) error, dest string, duration float64) error {
	part := dest + ".part"
	cmd := exec.Command("ffmpeg", "-y", "-loglevel", "error", "-f", "mpegts", "-i", "pipe:0",
		"-c", "copy", "-bsf:a", "aac_adtstoasc", "-movflags", "+faststart", "-f", "mp4", part)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}

	var stderrBuf bytes.Buffer
	stderrDone := make(chan struct{})
	go func() {
		io.Copy(&stderrBuf, stderrPipe)
		close(stderrDone)
	}()

	feedErr := make(chan error, 1)
	go func() { feedErr <- feed(stdin) }()

	waitErr := waitWithTimeout(cmd, 600*time.Second)
	<-stderrDone

	if err := <-feedErr; err != nil {
		os.Remove(part)
		return err
	}
	if waitErr != nil || !fileNonEmpty(part) {
		os.Remove(part)
		return fmt.Errorf("ffmpeg mux failed: %s", truncate(strings.TrimSpace(stderrBuf.String()), 300))
	}

	// Backstop: a truncated/error-muxed file must not be renamed into place.
	got, probeErr := ffprobeDuration(part)
	if probeErr != nil {
		got = 0
	}
	if got <= 0 || (duration > 0 && math.Abs(got-duration)/duration > 0.05) {
		os.Remove(part)
		return fmt.Errorf("muxed duration %.1fs vs playlist %.1fs", got, duration)
	}

	return os.Rename(part, dest)
}

// muxSegments concatenates already-downloaded HLS segments, in order,
// into dest via one local ffmpeg pass.
func muxSegments(segPaths []string, dest string, duration float64) error {
	return runMux(func(stdin io.WriteCloser) error {
		defer stdin.Close()
		for _, p := range segPaths {
			if err := copyFile(stdin, p); err != nil {
				return err
			}
		}
		return nil
	}, dest, duration)
}

func copyFile(w io.Writer, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.CopyBuffer(w, f, make([]byte, 1<<18))
	return err
}

func waitWithTimeout(cmd *exec.Cmd, timeout time.Duration) error {
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-time.After(timeout):
		cmd.Process.Kill()
		<-done
		return errors.New("ffmpeg timed out")
	}
}

func fileNonEmpty(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Size() > 0
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// ffprobeDuration returns a muxed file's duration in seconds, as a
// backstop against a truncated or corrupt mux.
func ffprobeDuration(path string) (float64, error) {
	out, err := exec.Command("ffprobe", "-v", "error", "-show_entries", "format=duration",
		"-of", "default=nw=1:nk=1", path).Output()
	if err != nil {
		return 0, err
	}
	return strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
}

// downloadHLSNative fetches the master and variant playlists, downloads
// every segment with hlsWorkers parallel connections (each worker owns its
// own client), and muxes them as they arrive so the local mux pass
// overlaps the network transfer instead of following it.
func downloadHLSNative(masterURL, destMP4 string) error {
	client, err := newHLSHTTPClient()
	if err != nil {
		return err
	}
	masterText, masterFinal, err := hlsGet(client, masterURL)
	if err != nil {
		return err
	}
	_, variantURI, ok := pickVariant(masterText)
	if !ok {
		return errors.New("no usable HLS variant in master playlist")
	}
	childURL, err := resolveURL(masterFinal, variantURI)
	if err != nil {
		return err
	}
	childText, childFinal, err := hlsGet(client, childURL)
	if err != nil {
		return err
	}
	segRel, duration, err := parsePlaylist(childText)
	if err != nil {
		return err
	}
	segURLs := make([]string, len(segRel))
	for i, rel := range segRel {
		u, err := resolveURL(childFinal, rel)
		if err != nil {
			return err
		}
		segURLs[i] = u
	}

	tmp, err := os.MkdirTemp(filepath.Dir(destMP4), "flhls-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	segPaths := make([]string, len(segURLs))
	ready := make([]chan struct{}, len(segURLs))
	for i := range segURLs {
		segPaths[i] = filepath.Join(tmp, fmt.Sprintf("seg_%05d.ts", i))
		ready[i] = make(chan struct{})
	}

	failed := make(chan struct{})
	var failOnce sync.Once
	var dlErr error
	markFailed := func(err error) {
		failOnce.Do(func() {
			dlErr = err
			close(failed)
		})
	}

	jobs := make(chan int)
	go func() {
		for i := range segURLs {
			jobs <- i
		}
		close(jobs)
	}()

	var wg sync.WaitGroup
	for w := 0; w < hlsWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			wc, err := newHLSHTTPClient()
			if err != nil {
				markFailed(err)
				return
			}
			for i := range jobs {
				select {
				case <-failed:
					return
				default:
				}
				if _, err := downloadSegment(wc, segURLs[i], segPaths[i]); err != nil {
					markFailed(fmt.Errorf("segment %d: %w", i, err))
					return
				}
				close(ready[i])
			}
		}()
	}

	err = runMux(func(stdin io.WriteCloser) error {
		defer stdin.Close()
		for i, p := range segPaths {
			select {
			case <-ready[i]:
			case <-failed:
				return dlErr
			}
			if err := copyFile(stdin, p); err != nil {
				return err
			}
		}
		return nil
	}, destMP4, duration)

	wg.Wait()
	if err != nil {
		return err
	}
	if dlErr != nil {
		return dlErr
	}
	return nil
}

// downloadVideoFFmpeg lets ffmpeg's own HLS demuxer fetch and mux the
// stream, for playlists the native parallel path can't handle.
func downloadVideoFFmpeg(masterURL, destMP4 string) error {
	part := destMP4 + ".part"
	cmd := exec.Command("ffmpeg", "-y", "-loglevel", "error", "-i", masterURL,
		"-c", "copy", "-bsf:a", "aac_adtstoasc", "-movflags", "+faststart", "-f", "mp4", part)
	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	err := cmd.Run()
	if err != nil || !fileNonEmpty(part) {
		os.Remove(part)
		return fmt.Errorf("ffmpeg failed: %s", truncate(strings.TrimSpace(stderrBuf.String()), 300))
	}
	return os.Rename(part, destMP4)
}

// Video muxes vzaarID's HLS stream to destMP4, atomically. Tries the
// parallel native path first, falling back to ffmpeg's own demuxer for
// playlists it can't handle.
func (c *Client) Video(vzaarID, destMP4 string) error {
	masterURL := fmt.Sprintf("https://view.vzaar.com/%s/adaptive.m3u8", vzaarID)
	if err := downloadHLSNative(masterURL, destMP4); err != nil {
		fmt.Fprintf(c.log, "      ! native HLS download failed (%v); using ffmpeg instead\n", err)
		return downloadVideoFFmpeg(masterURL, destMP4)
	}
	return nil
}
