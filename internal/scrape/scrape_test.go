package scrape

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/jbonadiman/futurelearn-downloader/internal/course"
	"github.com/jbonadiman/futurelearn-downloader/internal/fetch"
	"github.com/jbonadiman/futurelearn-downloader/internal/hls"
)

const testdataDir = "../testdata"

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func readFixture(t *testing.T, rel string) string {
	t.Helper()
	return readFile(t, filepath.Join(testdataDir, rel))
}

func TestStepCompleteAcceptsReferenceTree(t *testing.T) {
	root := filepath.Join(testdataDir, "reference-tree", "course")
	var checked int
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(p, ".md") {
			return err
		}
		checked++
		if !StepComplete(p) {
			t.Errorf("reference step reported incomplete: %s", p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked < 5 {
		t.Fatalf("only %d steps checked; fixture tree looks wrong", checked)
	}
}

func TestStepCompleteRejectsMissingAsset(t *testing.T) {
	dir := t.TempDir()
	md := filepath.Join(dir, "step.md")
	if err := os.WriteFile(md, []byte("[a](missing.pdf)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if StepComplete(md) {
		t.Fatal("step with a missing asset reported complete")
	}
}

func TestStepCompleteIgnoresSiblingStepLinks(t *testing.T) {
	dir := t.TempDir()
	md := filepath.Join(dir, "step.md")
	if err := os.WriteFile(md, []byte("[other](../other/Other.md)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !StepComplete(md) {
		t.Fatal("sibling .md link treated as an asset")
	}
}

// --- goldens: bind each manifest golden to its source step page ---

type manifestStep struct {
	Parts      []string `json:"parts"`
	Title      string   `json:"title"`
	Href       string   `json:"href"`
	StepNumber string   `json:"stepNumber"`
	Type       string   `json:"type"`
}

type manifestGolden struct {
	File     string `json:"file"`
	TreePath string `json:"tree_path"`
}

type manifest struct {
	Steps   []manifestStep   `json:"steps"`
	Goldens []manifestGolden `json:"goldens"`
}

func loadManifest(t *testing.T) manifest {
	t.Helper()
	var m manifest
	if err := json.Unmarshal([]byte(readFixture(t, "manifest.json")), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

var stepIDRE = regexp.MustCompile(`/steps/(\d+)$`)

type goldenFixture struct {
	GoldenPath string
	Step       course.Step
	PagePath   string
}

// goldenFixtures binds every non-ToC golden to the step page HTML it was
// rendered from: tree_path's directory chain (everything but the filename)
// is exactly a step's Parts, and its manifest href carries the step ID that
// names the page fixture (pages/step-<id>.html).
func goldenFixtures(t *testing.T) []goldenFixture {
	t.Helper()
	m := loadManifest(t)
	byParts := make(map[string]manifestStep, len(m.Steps))
	for _, s := range m.Steps {
		byParts[strings.Join(s.Parts, "/")] = s
	}

	var out []goldenFixture
	for _, g := range m.Goldens {
		if g.TreePath == "ToC.md" {
			continue
		}
		segs := strings.Split(g.TreePath, "/")
		key := strings.Join(segs[:len(segs)-1], "/")
		s, ok := byParts[key]
		if !ok {
			t.Fatalf("golden %s: no manifest step for parts %q", g.File, key)
		}
		id := stepIDRE.FindStringSubmatch(s.Href)
		if id == nil {
			t.Fatalf("golden %s: step href %q has no step id", g.File, s.Href)
		}
		out = append(out, goldenFixture{
			GoldenPath: filepath.Join(testdataDir, g.File),
			PagePath:   filepath.Join(testdataDir, "pages", "step-"+id[1]+".html"),
			Step: course.Step{
				StepNumber: s.StepNumber,
				Title:      s.Title,
				Href:       s.Href,
				Type:       s.Type,
				Parts:      s.Parts,
			},
		})
	}
	return out
}

func writeCookieFile(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "cookies.txt")
	if err := os.WriteFile(p, []byte("# Netscape HTTP Cookie File\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// noRelatedFileClient wraps a real *fetch.Client and forces every related-file
// download to fail. The fixtures' relatedFiles URLs point at a signed-token
// path with no captured content, so the golden output has no "## Downloads"
// section for these fixtures.
type noRelatedFileClient struct{ *fetch.Client }

func (noRelatedFileClient) RelatedFile(string, string, string, string, map[string]bool) (string, error) {
	return "", fmt.Errorf("not found")
}

// buildStepFromPage renders one step page through the real extraction,
// localisation and Markdown pipeline (processStep), with the fixture's CDN
// host redirected to a local httptest server so image/subtitle downloads
// resolve to real bytes instead of a non-existent domain.
func buildStepFromPage(t *testing.T, g goldenFixture) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "fixture-bytes")
	}))
	defer srv.Close()

	html := strings.ReplaceAll(readFile(t, g.PagePath), "https://cdn.Synthetic.test", srv.URL)

	fc, err := fetch.NewClient(fetch.Options{CookiesPath: writeCookieFile(t), Log: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	client := noRelatedFileClient{fc}

	md, _, err := processStep(client, html, g.Step, t.TempDir(), map[string]string{}, Config{}, map[string]bool{}, io.Discard)
	if err != nil {
		t.Fatalf("processStep(%s): %v", g.PagePath, err)
	}
	return md
}

// tokens collapses markdown to the shape that must match: headings, list items,
// link and image destinations, table rows, and blockquote markers.
var tokenRE = regexp.MustCompile(`(?m)^(#{1,6} .+)$|^\s*(?:[-*+]|\d+\.)\s+(.+)$|!\[[^\]]*\]\(([^)]+)\)|\[[^\]]*\]\(([^)]+)\)|^(\|.+\|)$|^(>+)\s`)

func tokens(md string) []string {
	var out []string
	for _, m := range tokenRE.FindAllStringSubmatch(md, -1) {
		for i := 1; i < len(m); i++ {
			if m[i] != "" {
				out = append(out, strings.Join(strings.Fields(m[i]), " "))
				break
			}
		}
	}
	return out
}

func TestGoldensMatchByStructure(t *testing.T) {
	for _, g := range goldenFixtures(t) {
		want := tokens(readFile(t, g.GoldenPath))
		got := tokens(buildStepFromPage(t, g))
		if len(got) == 0 {
			t.Fatalf("no tokens produced for %s", g.PagePath)
		}
		for i := 0; i < len(want) && i < len(got); i++ {
			if want[i] != got[i] {
				t.Fatalf("%s: token %d = %q, want %q", filepath.Base(g.GoldenPath), i, got[i], want[i])
			}
		}
		if len(want) != len(got) {
			t.Fatalf("%s: %d tokens, want %d", filepath.Base(g.GoldenPath), len(got), len(want))
		}
	}
}

// countingClient counts every call and fails each one, so any test using it
// asserts "the code under test never needed to fetch anything."
type countingClient struct{ n *int }

func (c countingClient) fail() error { *c.n++; return fmt.Errorf("unexpected request") }

func (c countingClient) Text(string) (string, error) { return "", c.fail() }
func (c countingClient) ToFile(string, string) (int64, error) {
	return 0, c.fail()
}
func (c countingClient) Pace() {}
func (c countingClient) RelatedFile(string, string, string, string, map[string]bool) (string, error) {
	return "", c.fail()
}
func (c countingClient) LocalizeAudio(string, string, string, map[string]bool) (string, []string, error) {
	return "", nil, c.fail()
}
func (c countingClient) LocalizeImages(string, string, string, map[string]bool) (string, error) {
	return "", c.fail()
}
func (c countingClient) LocalizeFiles(string, string, string, map[string]bool) (string, []string, error) {
	return "", nil, c.fail()
}
func (c countingClient) Subtitles(*course.Video, string, string) ([]string, error) {
	return nil, c.fail()
}
func (c countingClient) ResolveRelatedLinks(links []course.RelatedLink) []course.RelatedLink {
	*c.n++
	return links
}
func (c countingClient) Video(string, string) error { return c.fail() }
func (c countingClient) PrefetchVideo(string) (*hls.Playlist, error) {
	return nil, c.fail()
}
func (c countingClient) VideoResolved(string, string, *hls.Playlist) error { return c.fail() }

// videoPrefetchClient records, for every job, the playlist PrefetchVideo
// returned and the playlist VideoResolved was actually called with, so a
// test can assert each job receives its own prefetch result — not another
// job's, and not a stale one when its prefetch failed.
type videoPrefetchClient struct {
	Client
	playlistFor  map[string]*hls.Playlist
	failPrefetch map[string]bool

	mu         sync.Mutex
	prefetched []string
	resolved   []string
	mismatch   error
}

func (c *videoPrefetchClient) PrefetchVideo(vzaarID string) (*hls.Playlist, error) {
	c.mu.Lock()
	c.prefetched = append(c.prefetched, vzaarID)
	c.mu.Unlock()
	if c.failPrefetch[vzaarID] {
		return nil, fmt.Errorf("prefetch failed for %s", vzaarID)
	}
	return c.playlistFor[vzaarID], nil
}

func (c *videoPrefetchClient) VideoResolved(vzaarID, destMP4 string, playlist *hls.Playlist) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.resolved = append(c.resolved, vzaarID)
	switch {
	case c.failPrefetch[vzaarID] && playlist != nil:
		c.mismatch = fmt.Errorf("video %s: got a non-nil playlist after its own prefetch failed", vzaarID)
	case !c.failPrefetch[vzaarID] && playlist != c.playlistFor[vzaarID]:
		c.mismatch = fmt.Errorf("video %s: got playlist %p, want its own prefetch result %p", vzaarID, playlist, c.playlistFor[vzaarID])
	}
	return nil
}

func TestDownloadVideosPassesEachJobItsOwnPrefetchedPlaylist(t *testing.T) {
	const n = 6
	jobs := make([]videoJob, n)
	c := &videoPrefetchClient{playlistFor: map[string]*hls.Playlist{}, failPrefetch: map[string]bool{}}
	for i := range jobs {
		id := fmt.Sprintf("vid%d", i)
		jobs[i] = videoJob{VzaarID: id, Dest: id + ".mp4"}
		c.playlistFor[id] = &hls.Playlist{Duration: float64(i)}
	}
	c.failPrefetch["vid3"] = true // one prefetch fails; that job must still run, with a nil playlist

	downloadVideos(c, jobs, io.Discard)

	if c.mismatch != nil {
		t.Fatal(c.mismatch)
	}
	if len(c.resolved) != n {
		t.Fatalf("resolved %d videos, want %d", len(c.resolved), n)
	}
	for i, id := range c.resolved {
		if id != jobs[i].VzaarID {
			t.Fatalf("resolved out of order at %d: got %s, want %s", i, id, jobs[i].VzaarID)
		}
	}
}

func TestResumeOverReferenceTreeMakesNoRequests(t *testing.T) {
	// The reference tree covers the first 7 steps of this course-tree fixture
	// (the only ones with captured step pages), and a resumed run over an
	// already-complete tree must make zero requests. --limit 7 reproduces
	// that same subset in document order.
	out := t.TempDir()
	if err := copyDir(filepath.Join(testdataDir, "reference-tree", "course"), filepath.Join(out, "course")); err != nil {
		t.Fatal(err)
	}

	calls := 0
	cfg := Config{Out: out, Limit: 7}
	if err := RunOne(readFixture(t, "pages/course-tree.html"), countingClient{&calls}, cfg, io.Discard); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("resumed run made %d requests, want 0", calls)
	}
}

func copyDir(src, dst string) error {
	return filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o644)
	})
}

func TestRunOneDryRunReportsCountAndSkipsWrite(t *testing.T) {
	out := t.TempDir()
	var stdout strings.Builder
	cfg := Config{Out: out, DryRun: true, Limit: 2}
	if err := RunOne(readFixture(t, "pages/course-tree.html"), countingClient{new(int)}, cfg, &stdout); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "Would scrape 2 step(s) into:") {
		t.Fatalf("unexpected dry-run output: %s", stdout.String())
	}
	if _, err := os.Stat(filepath.Join(out, "course", "ToC.md")); !os.IsNotExist(err) {
		t.Fatalf("dry run wrote files: %v", err)
	}
}

func TestLocalizeStepLinksRewritesToRelativeMarkdownPath(t *testing.T) {
	linkMap := map[string]string{
		"/courses/c/1/steps/2": filepath.Join("Week 1", "Two.md"),
	}
	html := `<p>See <a href="/courses/c/1/steps/2#glossary">step two</a>.</p>`
	got := localizeStepLinks(html, linkMap, "Week 1")
	if !strings.Contains(got, `href="Two.md#glossary"`) {
		t.Fatalf("link not rewritten: %s", got)
	}
}

func TestLocalRefsSkipsRemoteAndStepCrossReferences(t *testing.T) {
	md := "[remote](https://example.com/a.pdf) [frag](#x) [abs](/root.pdf) " +
		"[local](file.pdf) [step](../Other/Other.md) <video src=\"clip.mp4\"></video>"
	refs := localRefs(md)
	want := []string{"file.pdf", "clip.mp4"}
	if len(refs) != len(want) {
		t.Fatalf("localRefs = %v, want %v", refs, want)
	}
	for i, r := range refs {
		if r != want[i] {
			t.Fatalf("localRefs[%d] = %q, want %q", i, r, want[i])
		}
	}
}

// textClient serves canned bodies and records what was fetched, so the fallback
// chain can be asserted without a server.
type textClient struct {
	Client
	bodies  map[string]string
	fetched []string
}

func (c *textClient) Text(target string) (string, error) {
	c.fetched = append(c.fetched, target)
	body, ok := c.bodies[target]
	if !ok {
		return "", fmt.Errorf("unexpected request: %s", target)
	}
	return body, nil
}

func (c *textClient) Pace() {}

const runBase = "https://www.futurelearn.com/courses/synthetic-course/1"

// A run's overview page carries no tree of its own, so the run's to-do page,
// which does list this run's steps, is followed to reach one.
func TestFetchCoursePageFallsBackToRunToDoPage(t *testing.T) {
	overview := `<a href="/info/courses/synthetic-course/0/steps/999999">highlight</a>` +
		`<a href="/courses/synthetic-course/1/todo">To do</a>`
	c := &textClient{bodies: map[string]string{
		runBase:                    overview,
		runBase + "/todo":          readFixture(t, "pages/course-todo.html"),
		runBase + "/steps/1992446": readFixture(t, "pages/course-tree.html"),
	}}

	html, err := fetchCoursePage(c, runBase, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if !course.HasCourseTree(html) {
		t.Fatal("fallback returned a page without the course tree")
	}
	for _, u := range c.fetched {
		if strings.Contains(u, "/info/") {
			t.Errorf("followed a cross-run highlights link: %s", u)
		}
	}
}

// An overview page also carries "course highlights" links pointing at a
// different run. Following one lands on an unrelated page with no tree, so it
// must be left alone even when nothing better is on offer.
func TestFetchCoursePageIgnoresCrossRunHighlightsLink(t *testing.T) {
	overview := `<a href="/info/courses/synthetic-course/0/steps/999999">highlight</a>`
	c := &textClient{bodies: map[string]string{runBase: overview}}

	html, err := fetchCoursePage(c, runBase, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if html != overview {
		t.Fatalf("expected the overview page back, got %d bytes", len(html))
	}
	for _, u := range c.fetched {
		if strings.Contains(u, "999999") {
			t.Errorf("followed a cross-run step link: %s", u)
		}
	}
}

// A page that already carries the tree costs exactly one request.
func TestFetchCoursePageUsesTreeInPlace(t *testing.T) {
	c := &textClient{bodies: map[string]string{
		runBase: readFixture(t, "pages/course-tree.html"),
	}}

	html, err := fetchCoursePage(c, runBase, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if !course.HasCourseTree(html) {
		t.Fatal("returned a page without the course tree")
	}
	if len(c.fetched) != 1 {
		t.Fatalf("fetched %v, want only the requested page", c.fetched)
	}
}
