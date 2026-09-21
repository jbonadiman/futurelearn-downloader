package main

import (
	"bytes"
	"errors"
	"flag"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFlagValidation(t *testing.T) {
	for _, args := range [][]string{
		{}, // no URL and no --links
		{"--cookies", "c.txt", "https://x", "--links", "l.txt"}, // both
	} {
		if err := run(args, io.Discard); err == nil {
			t.Errorf("args %v accepted, want an error", args)
		}
	}
}

func TestVersionFlag(t *testing.T) {
	version = "v9.9.9-test"
	t.Cleanup(func() { version = "dev" })
	var buf bytes.Buffer
	if err := run([]string{"--version"}, &buf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "v9.9.9-test") {
		t.Fatalf("output = %q", buf.String())
	}
}

// main() turns this sentinel into exit 0 rather than a "!" error line.
func TestHelpIsNotAnError(t *testing.T) {
	if err := run([]string{"--help"}, io.Discard); !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("run(--help) = %v, want flag.ErrHelp", err)
	}
}

func TestReadLinksSkipsBlanksAndComments(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "links.txt")
	os.WriteFile(p, []byte("# comment\n\nhttps://a.test\nhttps://b.test\n"), 0o644)
	got, err := readLinks(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %v", got)
	}
}

func TestReadLinksDropsDuplicatesPreservingOrder(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "links.txt")
	os.WriteFile(p, []byte("https://a.test\nhttps://b.test\nhttps://a.test\n"), 0o644)
	got, err := readLinks(p)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"https://a.test", "https://b.test"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestFlagCourseURLAfterFlags(t *testing.T) {
	rest, positional, err := splitPositional([]string{"--cookies", "c.txt", "https://x", "--dry-run"})
	if err != nil {
		t.Fatal(err)
	}
	if positional != "https://x" {
		t.Fatalf("positional = %q", positional)
	}
	want := []string{"--cookies", "c.txt", "--dry-run"}
	if len(rest) != len(want) {
		t.Fatalf("rest = %v", rest)
	}
	for i, w := range want {
		if rest[i] != w {
			t.Fatalf("rest[%d] = %q, want %q", i, rest[i], w)
		}
	}
}

// TestResumeOverReferenceTreeMakesNoRequests runs the real binary entry
// point end to end against the committed reference tree: the course-tree
// page is served locally (no live futurelearn.com access), --out already
// holds the reference tree's steps, and --limit 7 reproduces the same
// first-7-steps subset the reference tree was generated from (see
// internal/scrape's TestResumeOverReferenceTreeMakesNoRequests for why).
// Every step is already complete, so the run should skip all 7 without
// erroring — it never needs the cookies file to actually authenticate.
func TestResumeOverReferenceTreeMakesNoRequests(t *testing.T) {
	out := t.TempDir()
	if err := copyDir(filepath.Join("internal", "testdata", "reference-tree", "course"), filepath.Join(out, "course")); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.FileServer(http.Dir(filepath.Join("internal", "testdata", "pages"))))
	defer srv.Close()

	cookies := filepath.Join(out, "cookies.txt")
	if err := os.WriteFile(cookies, []byte("# Netscape HTTP Cookie File\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	args := []string{
		"--cookies", cookies,
		"--out", out,
		"--limit", "7",
		srv.URL + "/course-tree.html",
	}
	if err := run(args, &buf); err != nil {
		t.Fatalf("run: %v\noutput: %s", err, buf.String())
	}
	if got := strings.Count(buf.String(), "-- complete, skipping"); got != 7 {
		t.Fatalf("got %d steps skipped as complete, want 7:\n%s", got, buf.String())
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
