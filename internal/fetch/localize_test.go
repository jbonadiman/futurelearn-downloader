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

	"github.com/jbonadiman/futurelearn-downloader/internal/course"
)

func testClient(t *testing.T, _ string) *Client {
	t.Helper()
	c, err := NewClient(Options{CookiesPath: writeCookieFile(t), Delay: 0, Log: io.Discard})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c
}

func fixedBodyServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, body)
	}))
}

func pngServer(t *testing.T) *httptest.Server     { return fixedBodyServer(t, "\x89PNG-fake-bytes") }
func mp3Server(t *testing.T) *httptest.Server     { return fixedBodyServer(t, "ID3-fake-mp3-bytes") }
func pdfLinkServer(t *testing.T) *httptest.Server { return fixedBodyServer(t, "%PDF-fake-bytes") }

func TestLocalizeImagesRewritesSrcAndKeepsAlt(t *testing.T) {
	srv := pngServer(t)
	c := testClient(t, srv.URL)
	dir := t.TempDir()
	used := map[string]bool{}
	html := `<p><img src="` + srv.URL + `/hero.png" alt="A hero"></p>`
	out, err := c.LocalizeImages(html, dir, "base", used)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, srv.URL) {
		t.Fatalf("remote src survived: %s", out)
	}
	if !strings.Contains(out, `src="hero.png"`) || !strings.Contains(out, `alt="A hero"`) {
		t.Fatalf("rewritten html: %s", out)
	}
	if _, err := os.Stat(filepath.Join(dir, "hero.png")); err != nil {
		t.Fatalf("image not written: %v", err)
	}
}

func TestLocalizeAudioReturnsDownloadedNames(t *testing.T) {
	srv := mp3Server(t)
	c := testClient(t, srv.URL)
	dir := t.TempDir()
	html, names, err := c.LocalizeAudio(`<span class="soundcite" data-url="`+srv.URL+`/clip.mp3">x</span>`, dir, "base", map[string]bool{})
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 1 || !strings.HasSuffix(names[0], ".mp3") {
		t.Fatalf("names = %v", names)
	}
	if !strings.Contains(html, names[0]) {
		t.Fatalf("data-url not rewritten to the local file: %s", html)
	}
}

func TestRelatedFileUsesTypeForExtension(t *testing.T) {
	// FutureLearn's /links/… redirects have no extension; the type field supplies it.
	srv := pdfLinkServer(t)
	c := testClient(t, srv.URL)
	dir := t.TempDir()
	name, err := c.RelatedFile(srv.URL, "pdf", "Handout", dir, map[string]bool{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(name, ".pdf") {
		t.Fatalf("name = %q", name)
	}
}

func TestRelatedFileDerivesExtensionFromFinalURLWhenTypeUnknown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/links/f/1" {
			http.Redirect(w, r, "/real/handout.docx?sig=abc", http.StatusFound)
			return
		}
		fmt.Fprint(w, "docx-bytes")
	}))
	defer srv.Close()
	c := testClient(t, srv.URL)
	dir := t.TempDir()
	name, err := c.RelatedFile(srv.URL+"/links/f/1", "", "Handout", dir, map[string]bool{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(name, ".docx") {
		t.Fatalf("name = %q, want .docx derived from the redirect target", name)
	}
}

func TestRelatedFileSkipsRequestWhenAlreadyDownloaded(t *testing.T) {
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		fmt.Fprint(w, "bytes")
	}))
	defer srv.Close()
	c := testClient(t, srv.URL)
	dir := t.TempDir()
	used := map[string]bool{}

	if _, err := c.RelatedFile(srv.URL, "pdf", "Handout", dir, used); err != nil {
		t.Fatal(err)
	}
	if requests != 1 {
		t.Fatalf("first call made %d requests, want 1", requests)
	}

	if _, err := c.RelatedFile(srv.URL, "pdf", "Handout", dir, map[string]bool{}); err != nil {
		t.Fatal(err)
	}
	if requests != 1 {
		t.Fatalf("second call against an existing file made another request: %d total", requests)
	}
}

func TestLocalizeAudioDropsClipOnDownloadFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	c := testClient(t, srv.URL)
	dir := t.TempDir()
	html := `<a href="` + srv.URL + `/clip.mp3">clip</a>`
	out, names, err := c.LocalizeAudio(html, dir, "base", map[string]bool{})
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 0 {
		t.Fatalf("names = %v, want none (failed download must not be reserved)", names)
	}
	if !strings.Contains(out, srv.URL) {
		t.Fatalf("failed clip's URL must survive unrewritten: %s", out)
	}
}

func TestLocalizeImagesReservesNameEvenOnDownloadFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	c := testClient(t, srv.URL)
	dir := t.TempDir()
	used := map[string]bool{}
	html := `<img src="` + srv.URL + `/hero.png">`
	out, err := c.LocalizeImages(html, dir, "base", used)
	if err != nil {
		t.Fatal(err)
	}
	if !used["hero.png"] {
		t.Fatal("failed image download must still reserve its name")
	}
	if !strings.Contains(out, srv.URL) {
		t.Fatalf("failed image's URL must survive unrewritten: %s", out)
	}
}

func TestSubtitlesDeduplicatesLanguageIndex(t *testing.T) {
	srv := fixedBodyServer(t, "WEBVTT\n\n1\n00:00:00.000 --> 00:00:01.000\nHi.\n")
	c := testClient(t, srv.URL)
	dir := t.TempDir()
	video := &course.Video{Subtitles: []course.Subtitle{
		{SrcLang: "en", Src: srv.URL},
		{SrcLang: "en", Src: srv.URL},
	}}
	names, err := c.Subtitles(video, dir, "Title")
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 2 || names[0] != "Title.en.vtt" || names[1] != "Title.en.1.vtt" {
		t.Fatalf("names = %v", names)
	}
	for _, n := range names {
		if _, err := os.Stat(filepath.Join(dir, n)); err != nil {
			t.Fatalf("subtitle not written: %v", err)
		}
	}
}

func TestSubtitlesKeepsNameOnFetchFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	c := testClient(t, srv.URL)
	dir := t.TempDir()
	video := &course.Video{Subtitles: []course.Subtitle{{SrcLang: "en", Src: srv.URL}}}
	names, err := c.Subtitles(video, dir, "Title")
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 1 || names[0] != "Title.en.vtt" {
		t.Fatalf("names = %v, want [Title.en.vtt] even though the fetch failed", names)
	}
	if _, err := os.Stat(filepath.Join(dir, "Title.en.vtt")); err == nil {
		t.Fatal("subtitle file should not exist after a failed fetch")
	}
}

func TestResolveRelatedLinksRewritesToFinalURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/links/l/1" {
			http.Redirect(w, r, "/real-destination", http.StatusFound)
			return
		}
		fmt.Fprint(w, "ok")
	}))
	defer srv.Close()
	c := testClient(t, srv.URL)

	out := c.ResolveRelatedLinks([]course.RelatedLink{
		{Title: "Docs", URL: srv.URL + "/links/l/1"},
		{Title: "", URL: srv.URL + "/links/l/1"},
	})
	if len(out) != 2 {
		t.Fatalf("out = %v", out)
	}
	if !strings.HasSuffix(out[0].URL, "/real-destination") {
		t.Fatalf("url = %q", out[0].URL)
	}
	wantTitle := srv.URL + "/links/l/1"
	if out[1].Title != wantTitle {
		t.Fatalf("empty title should fall back to the pre-resolution url %q, got %q", wantTitle, out[1].Title)
	}
	if !strings.HasSuffix(out[1].URL, "/real-destination") {
		t.Fatalf("url = %q", out[1].URL)
	}
}

func TestFinalURLFallsBackToOriginalOnFailure(t *testing.T) {
	c := testClient(t, "http://127.0.0.1:1")
	got := c.FinalURL("http://127.0.0.1:1/dead")
	if got != "http://127.0.0.1:1/dead" {
		t.Fatalf("got %q, want the original URL unchanged", got)
	}
}
