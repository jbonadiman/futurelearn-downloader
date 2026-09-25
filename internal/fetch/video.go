package fetch

import (
	"fmt"
	"net/url"
	"os"
	"regexp"

	"github.com/jbonadiman/futurelearn-downloader/internal/hls"
)

// fileNonEmpty reports whether path exists and holds at least one byte.
// Used by the localise helpers to tell a finished download from a stub.
func fileNonEmpty(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Size() > 0
}

// vzaarIDPattern matches valid vzaar video IDs: alphanumeric only, no path traversal.
var vzaarIDPattern = regexp.MustCompile(`^[a-zA-Z0-9]+$`)

// Video downloads vzaarID's HLS stream to destMP4, atomically. The playlist,
// segment and ffmpeg work lives in package hls; fetch owns the vzaar URL
// because that is the FutureLearn-specific part of it.
func (c *Client) Video(vzaarID, destMP4 string) error {
	if !vzaarIDPattern.MatchString(vzaarID) {
		return fmt.Errorf("invalid vzaarID: must be alphanumeric only, got %q", vzaarID)
	}
	masterURL := fmt.Sprintf("https://view.vzaar.com/%s/adaptive.m3u8", vzaarID)
	if err := validateMasterURL(masterURL); err != nil {
		return fmt.Errorf("invalid master URL: %w", err)
	}
	return hls.Download(masterURL, destMP4, c.log)
}

// validateMasterURL ensures the URL is HTTPS and from the expected domain.
func validateMasterURL(masterURL string) error {
	u, err := url.Parse(masterURL)
	if err != nil {
		return fmt.Errorf("parse: %w", err)
	}
	if u.Scheme != "https" {
		return fmt.Errorf("scheme must be https, got %q", u.Scheme)
	}
	if u.Host != "view.vzaar.com" {
		return fmt.Errorf("host must be view.vzaar.com, got %q", u.Host)
	}
	return nil
}
