package fetch

import (
	"fmt"
	"os"

	"github.com/jbonadiman/futurelearn-downloader/internal/hls"
)

// fileNonEmpty reports whether path exists and holds at least one byte.
// Used by the localise helpers to tell a finished download from a stub.
func fileNonEmpty(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Size() > 0
}

// Video downloads vzaarID's HLS stream to destMP4, atomically. The playlist,
// segment and ffmpeg work lives in package hls; fetch owns the vzaar URL
// because that is the FutureLearn-specific part of it.
func (c *Client) Video(vzaarID, destMP4 string) error {
	masterURL := fmt.Sprintf("https://view.vzaar.com/%s/adaptive.m3u8", vzaarID)
	return hls.Download(masterURL, destMP4, c.log)
}
