package hls

import (
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func readFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("../../internal/testdata/" + name)
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	return string(b)
}

func segmentPaths(t *testing.T) []string {
	t.Helper()
	names := []string{"seg000.ts", "seg001.ts", "seg002.ts"}
	paths := make([]string, len(names))
	for i, n := range names {
		abs, err := filepath.Abs("../../internal/testdata/media/" + n)
		if err != nil {
			t.Fatalf("abs: %v", err)
		}
		paths[i] = abs
	}
	return paths
}

func probeDuration(t *testing.T, path string) float64 {
	t.Helper()
	got, err := ffprobeDuration(path)
	if err != nil {
		t.Fatalf("ffprobe: %v", err)
	}
	return got
}

func TestPickVariantIgnoresIFrame(t *testing.T) {
	master := readFixture(t, "media/master.m3u8")
	uri, ok := pickVariant(master)
	if !ok {
		t.Fatal("no variant")
	}
	if uri != "video-720p.m3u8?context=SYNTHETICCONTEXT" {
		t.Fatalf("picked %q, want video-720p", uri)
	}
}

func TestParsePlaylistRejectsEncryptedAndLive(t *testing.T) {
	if _, _, err := parsePlaylist("#EXTM3U\n#EXT-X-KEY:METHOD=AES-128\n#EXTINF:2,\na.ts\n#EXT-X-ENDLIST"); err == nil || !strings.Contains(err.Error(), "EXT-X-KEY") {
		t.Fatalf("encrypted playlist accepted: %v", err)
	}
	if _, _, err := parsePlaylist("#EXTM3U\n#EXTINF:2,\na.ts"); err == nil || !strings.Contains(err.Error(), "ENDLIST") {
		t.Fatalf("live playlist accepted: %v", err)
	}
}

func TestParsePlaylistRejectsEmptySegmentList(t *testing.T) {
	if _, _, err := parsePlaylist("#EXTM3U\n#EXT-X-ENDLIST"); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("empty segment list accepted: %v", err)
	}
}

func TestParsePlaylistFromRealVariant(t *testing.T) {
	variant := readFixture(t, "media/variant.m3u8")
	segs, duration, err := parsePlaylist(variant)
	if err != nil {
		t.Fatalf("parsePlaylist: %v", err)
	}
	if len(segs) != 3 {
		t.Fatalf("segments = %d, want 3", len(segs))
	}
	if math.Abs(duration-6.0) > 0.001 {
		t.Fatalf("duration = %v, want 6.0", duration)
	}
	if segs[0] != "seg000.ts?context=SYNTHETICCONTEXT&token=unsigned" {
		t.Fatalf("seg0 = %q", segs[0])
	}
}

func TestMuxSyntheticSegments(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	dir := t.TempDir()
	dest := filepath.Join(dir, "out.mp4")
	if err := muxSegments(segmentPaths(t), dest, 6.0); err != nil {
		t.Fatalf("mux: %v", err)
	}
	dur := probeDuration(t, dest)
	if math.Abs(dur-6.0) > 0.3 {
		t.Fatalf("muxed duration %.2fs, want ~6.0s", dur)
	}
}

func TestMuxRejectsDurationMismatch(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	dir := t.TempDir()
	dest := filepath.Join(dir, "out.mp4")
	err := muxSegments(segmentPaths(t), dest, 60.0)
	if err == nil || !strings.Contains(err.Error(), "muxed duration") {
		t.Fatalf("expected duration-mismatch error, got: %v", err)
	}
	if fileNonEmpty(dest) {
		t.Fatal("mismatched-duration mux must not be renamed into place")
	}
}

func TestDownloadHLSNativeEndToEndAgainstHTTPServer(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/adaptive.m3u8", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(readFixture(t, "media/master.m3u8")))
	})
	mux.HandleFunc("/video-720p.m3u8", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(readFixture(t, "media/variant.m3u8")))
	})
	for _, n := range []string{"seg000.ts", "seg001.ts", "seg002.ts"} {
		n := n
		mux.HandleFunc("/"+n, func(w http.ResponseWriter, r *http.Request) {
			b, _ := os.ReadFile("../../internal/testdata/media/" + n)
			w.Write(b)
		})
	}
	srv := httptest.NewServer(mux)
	defer srv.Close()

	dir := t.TempDir()
	dest := filepath.Join(dir, "out.mp4")
	if err := downloadHLSNative(srv.URL+"/adaptive.m3u8", dest); err != nil {
		t.Fatalf("downloadHLSNative: %v", err)
	}
	dur := probeDuration(t, dest)
	if math.Abs(dur-6.0) > 0.3 {
		t.Fatalf("muxed duration %.2fs, want ~6.0s", dur)
	}
}

func TestDownloadHLSNativeRejectsIFrameOnlyMaster(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("#EXTM3U\n#EXT-X-I-FRAME-STREAM-INF:BANDWIDTH=1,URI=\"x.m3u8\"\n"))
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "out.mp4")
	err := downloadHLSNative(srv.URL, dest)
	if err == nil || !strings.Contains(err.Error(), "no usable HLS variant") {
		t.Fatalf("expected 'no usable HLS variant' error, got: %v", err)
	}
}
