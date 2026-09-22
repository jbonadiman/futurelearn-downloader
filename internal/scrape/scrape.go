// Package scrape orchestrates one course: fetch every step page, localise
// its assets and links, render Markdown, defer videos to a cookie-less
// second phase, and write a ToC.
package scrape

import (
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/jbonadiman/futurelearn-downloader/internal/course"
	"github.com/jbonadiman/futurelearn-downloader/internal/fetch"
	"github.com/jbonadiman/futurelearn-downloader/internal/markdown"
	"github.com/jbonadiman/futurelearn-downloader/internal/quiz"
)

// newClient builds the real *fetch.Client used by Run, wired to cfg's
// cookies/delay and to stdout so fetch-internal failures (subtitle/download/
// audio/image) interleave with RunOne's own progress lines.
func newClient(cfg Config, stdout io.Writer) (*fetch.Client, error) {
	return fetch.NewClient(fetch.Options{
		CookiesPath: cfg.CookiesPath,
		Delay:       cfg.Delay,
		Log:         stdout,
	})
}

// Client is everything Run/RunOne need from an HTTP client — the surface
// *fetch.Client already implements.
type Client interface {
	Text(target string) (string, error)
	ToFile(target, dest string) (int64, error)
	Pace()
	RelatedFile(linkURL, ftype, titleSane, folder string, used map[string]bool) (string, error)
	LocalizeAudio(bodyHTML, folder, base string, used map[string]bool) (string, []string, error)
	LocalizeImages(bodyHTML, folder, base string, used map[string]bool) (string, error)
	LocalizeFiles(bodyHTML, folder, base string, used map[string]bool) (string, []string, error)
	Subtitles(video *course.Video, folder, base string) ([]string, error)
	ResolveRelatedLinks(links []course.RelatedLink) []course.RelatedLink
	Video(vzaarID, destMP4 string) error
}

// Config holds one field per CLI flag.
type Config struct {
	CourseURL     string
	CookiesPath   string
	Out           string
	Limit         int
	Delay         time.Duration
	SkipVideo     bool
	SkipSubs      bool
	SkipDownloads bool
	SkipAudio     bool
	SkipQuiz      bool
	Force         bool
	SkipLocked    bool
	DryRun        bool
}

// mdLinkRE/mdSrcRE/remoteDestRE support localRefs: a step page's own
// Markdown link targets and <audio|video src> attributes double as its
// manifest.
var (
	mdLinkRE     = regexp.MustCompile(`\[[^\]\n]*\]\(([^()\n]*(?:\([^()\n]*\)[^()\n]*)*)\)`)
	mdSrcRE      = regexp.MustCompile(`<(?:audio|video)\b[^>]*\bsrc="([^"]*)"`)
	remoteDestRE = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9+.-]*:|^#|^/`)
)

// localRefs returns the local files a step page references (link targets
// + <video>/<audio> src), excluding remote destinations and
// cross-references to *other* step pages (.md/.markdown) — those aren't
// assets this step produced, so they must not gate resume.
func localRefs(mdText string) []string {
	var out []string
	matches := append(mdLinkRE.FindAllStringSubmatch(mdText, -1), mdSrcRE.FindAllStringSubmatch(mdText, -1)...)
	for _, match := range matches {
		dest := match[1]
		if remoteDestRE.MatchString(dest) {
			continue
		}
		unq, err := url.PathUnescape(dest)
		if err != nil {
			unq = dest
		}
		lower := strings.ToLower(unq)
		if strings.HasSuffix(lower, ".md") || strings.HasSuffix(lower, ".markdown") {
			continue
		}
		out = append(out, unq)
	}
	return out
}

// StepComplete reports whether a step needs no further work, so a
// resumed run can skip its fetches. The Markdown is written last and
// links every asset the step produced, so it works as the manifest: page
// present and non-empty, and every file it points at present and
// non-empty.
func StepComplete(mdPath string) bool {
	info, err := os.Stat(mdPath)
	if err != nil || info.Size() == 0 {
		return false
	}
	b, err := os.ReadFile(mdPath)
	if err != nil {
		return false
	}
	folder := filepath.Dir(mdPath)
	for _, rel := range localRefs(string(b)) {
		fi, err := os.Stat(filepath.Join(folder, rel))
		if err != nil || fi.Size() == 0 {
			return false
		}
	}
	return true
}

// buildStepLinkMap maps each step's URL (relative and absolute) to its
// local Markdown path, so glossary/"see step N.M" links in body HTML can
// be rewritten to point at the local file instead of the live course URL.
func buildStepLinkMap(items []course.Step) map[string]string {
	linkMap := make(map[string]string, len(items)*2)
	for _, s := range items {
		if s.Href == "" {
			continue
		}
		rel := filepath.Join(filepath.Join(s.Parts...), course.Sanitize(s.Title)+".md")
		linkMap[s.Href] = rel
		if strings.HasPrefix(s.Href, "/") {
			linkMap[course.BaseURL+s.Href] = rel
		}
	}
	return linkMap
}

var hrefAttrRE = regexp.MustCompile(`(?i)href\s*=\s*(["'])`)

// localizeStepLinks rewrites step links in body HTML to local relative
// Markdown paths. fromDir is the folder (relative to the course root)
// holding the page being generated. Go's RE2 has no backreferences for a
// `(["'])(.*?)\1`-style pattern, so the opening/closing quote is matched
// manually instead.
func localizeStepLinks(htmlText string, linkMap map[string]string, fromDir string) string {
	locs := hrefAttrRE.FindAllStringSubmatchIndex(htmlText, -1)
	if locs == nil {
		return htmlText
	}
	var out strings.Builder
	last := 0
	for _, loc := range locs {
		quote := htmlText[loc[2]:loc[3]]
		valStart := loc[1]
		idx := strings.Index(htmlText[valStart:], quote)
		if idx < 0 {
			continue
		}
		valEnd := valStart + idx
		full := htmlText[loc[0] : valEnd+len(quote)]
		attrURL := htmlText[valStart:valEnd]

		target, frag, hasFrag := strings.Cut(attrURL, "#")
		repl := full
		if rel, ok := linkMap[target]; ok {
			relPath, err := filepath.Rel(fromDir, rel)
			if err == nil {
				newDest := markdown.URLPath(relPath)
				if hasFrag {
					newDest += "#" + frag
				}
				repl = "href=" + quote + newDest + quote
			}
		}

		out.WriteString(htmlText[last:loc[0]])
		out.WriteString(repl)
		last = valEnd + len(quote)
	}
	out.WriteString(htmlText[last:])
	return out.String()
}

// scrapeQuiz fetches a Quiz/Test step's questions and renders them as
// Markdown checkbox lists. Answers are only known once a question has
// been attempted on FutureLearn (gaveCorrectAnswer stays null otherwise),
// so an unattempted quiz renders with empty checkboxes — nothing here
// ever submits one.
func scrapeQuiz(client Client, stepURL, folder, base string, used map[string]bool, stdout io.Writer) []string {
	// /quiz/introduction is requested explicitly: once a quiz has been
	// started, the bare step URL serves the current *question* instead,
	// whose blob has no quiz introduction.
	introPage, err := client.Text(stepURL + "/quiz/introduction")
	if err != nil {
		fmt.Fprintf(stdout, "      ! quiz fetch failed: %v\n", err)
		return nil
	}
	meta, err := quiz.ExtractQuiz(introPage)
	if err != nil {
		fmt.Fprintf(stdout, "      ! quiz fetch failed: %v\n", err)
		return nil
	}

	lines := []string{"## Quiz", ""}
	if meta.IntroductionHTML != "" {
		localized, audio, err := client.LocalizeAudio(meta.IntroductionHTML, folder, base+"-intro", used)
		if err == nil {
			if intro := markdown.BodyToMarkdown(localized, audio); intro != "" {
				lines = append(lines, intro, "")
			}
		}
	}
	if meta.Weighting != "" {
		lines = append(lines, "*"+meta.Weighting+"*", "")
	}

	for n := 1; n <= meta.QuestionCount; n++ {
		text, err := client.Text(fmt.Sprintf("%s/questions/%d", stepURL, n))
		if err != nil {
			fmt.Fprintf(stdout, "      ! question %d failed: %v\n", n, err)
			continue
		}
		q, ok := quiz.ExtractQuestion(text)
		if !ok {
			continue
		}
		fmt.Fprintf(stdout, "      question %d/%d (%s)\n", n, meta.QuestionCount, q.Type)

		var audioNames []string
		if q.IntroductionHTML != "" {
			localized, audio, err := client.LocalizeAudio(q.IntroductionHTML, folder, fmt.Sprintf("%s-q%d", base, n), used)
			if err == nil {
				q.IntroductionHTML = localized
				audioNames = audio
			}
		}
		lines = append(lines, q.Markdown(n, audioNames...))
		client.Pace()
	}
	return lines
}

// processStep fetches nothing itself: html is the already-fetched step
// page. It extracts, localises and renders one step. The page fetch and
// file write are RunOne's own responsibility, so it can apply the resume
// check and progress lines around them.
func processStep(client Client, html string, step course.Step, folder string, linkMap map[string]string, cfg Config, used map[string]bool, stdout io.Writer) (mdText string, video *course.Video, err error) {
	data, err := course.ExtractStep(html)
	if err != nil {
		return "", nil, err
	}

	if len(data.RelatedLinks) > 0 {
		data.RelatedLinks = client.ResolveRelatedLinks(data.RelatedLinks)
	}

	titleSane := course.Sanitize(step.Title)

	var subLinks []string
	if data.Video != nil && !cfg.SkipSubs {
		subLinks, _ = client.Subtitles(data.Video, folder, titleSane)
	}

	var downloadLinks []markdown.DownloadLink
	if len(data.RelatedFiles) > 0 && !cfg.SkipDownloads {
		for _, file := range data.RelatedFiles {
			label := file.Title
			if label == "" {
				label = "download"
			}
			fname, err := client.RelatedFile(course.BaseURL+file.URL, file.Type, course.Sanitize(label), folder, used)
			if err != nil {
				fmt.Fprintf(stdout, "      ! download failed: %v\n", err)
				continue
			}
			downloadLinks = append(downloadLinks, markdown.DownloadLink{Label: label, Filename: fname})
			fmt.Fprintf(stdout, "      download -> %s\n", fname)
		}
	}

	// Inline audio clips live in the body text rather than in relatedFiles,
	// so they need localising separately.
	var audioFiles []string
	if !cfg.SkipAudio && data.BodyHTML != "" {
		data.BodyHTML, audioFiles, _ = client.LocalizeAudio(data.BodyHTML, folder, titleSane, used)
	}

	if data.BodyHTML != "" {
		data.BodyHTML, _ = client.LocalizeImages(data.BodyHTML, folder, titleSane, used)
		if !cfg.SkipDownloads {
			data.BodyHTML, _, _ = client.LocalizeFiles(data.BodyHTML, folder, titleSane, used)
		}
		data.BodyHTML = localizeStepLinks(data.BodyHTML, linkMap, filepath.Join(step.Parts...))
	}

	var quizLines []string
	if !cfg.SkipQuiz && (step.Type == "Quiz" || step.Type == "Test") {
		quizLines = scrapeQuiz(client, course.BaseURL+step.Href, folder, titleSane, used, stdout)
	}

	md := markdown.BuildStepMarkdown(markdown.StepInput{
		Title:         step.Title,
		TitleSane:     titleSane,
		BodyHTML:      data.BodyHTML,
		Copyright:     data.Copyright,
		Video:         data.Video,
		RelatedLinks:  data.RelatedLinks,
		SubLinks:      subLinks,
		DownloadLinks: downloadLinks,
		AudioFiles:    audioFiles,
		QuizLines:     quizLines,
	})
	return md, data.Video, nil
}

type videoJob struct{ VzaarID, Dest string }

// RunOne scrapes a single course from a page whose HTML embeds the
// course tree.
func RunOne(treeHTML string, client Client, cfg Config, stdout io.Writer) error {
	weeks, err := course.ExtractWeeks(treeHTML)
	if err != nil {
		return fmt.Errorf("%w Pass a step URL from the course instead — a run's overview page does not always carry the content tree", err)
	}
	rootName := course.Sanitize(course.RunTitle(treeHTML))
	root := filepath.Join(cfg.Out, rootName)
	items := course.CollectSteps(weeks, !cfg.SkipLocked)
	locked := course.LockedWeeks(weeks)
	sourceURL := course.SourceURL(items)

	if len(locked) > 0 {
		verb := "scraping anyway"
		if cfg.SkipLocked {
			verb = "skipping"
		}
		fmt.Fprintf(stdout, "WARNING: %d week(s) not yet released to you — %s:\n", len(locked), verb)
		for _, week := range locked {
			line := fmt.Sprintf("  - Week %d: %s", week.Number, week.FolderName)
			if week.UnlocksAt != "" {
				line += fmt.Sprintf("  (unlocks %s)", week.UnlocksAt)
			}
			fmt.Fprintln(stdout, line)
		}
		if cfg.SkipLocked {
			fmt.Fprint(stdout, "Re-run after they open; completed steps are skipped, so only the new "+
				"weeks are fetched.\n\n")
		} else {
			fmt.Fprint(stdout, "WARNING: these weeks are gated in the UI but their content is still\n"+
				"         served to an enrolled session, so it is being downloaded now.\n"+
				"         Pass --skip-locked to take only what has been released.\n\n")
		}
	}

	// Folders for every collected step are created up front, before --limit
	// truncates the work list.
	for _, step := range items {
		if err := os.MkdirAll(filepath.Join(root, filepath.Join(step.Parts...)), 0o755); err != nil {
			return err
		}
	}

	if cfg.Limit > 0 && cfg.Limit < len(items) {
		items = items[:cfg.Limit]
	}

	if cfg.DryRun {
		fmt.Fprintf(stdout, "Would scrape %d step(s) into: %s\n", len(items), root)
		return nil
	}

	linkMap := buildStepLinkMap(items)
	var videoJobs []videoJob
	skipped := 0

	// ---- Phase 1: pages + subtitles + downloads (needs cookies) ----
	for i, step := range items {
		folder := filepath.Join(root, filepath.Join(step.Parts...))
		titleSane := course.Sanitize(step.Title)
		mdPath := filepath.Join(folder, titleSane+".md")
		// Every asset written into this step's folder, so downloads/audio
		// can't collide (e.g. "u" and "ü" both sanitise to "u").
		used := map[string]bool{}

		if !cfg.Force && StepComplete(mdPath) {
			fmt.Fprintf(stdout, "[%d/%d] %s %s -- complete, skipping\n", i+1, len(items), step.StepNumber, step.Title)
			skipped++
			continue
		}
		fmt.Fprintf(stdout, "[%d/%d] %s %s\n", i+1, len(items), step.StepNumber, step.Title)

		html, err := client.Text(course.BaseURL + step.Href)
		if err != nil {
			fmt.Fprintf(stdout, "      ! fetch failed: %v\n", err)
			continue
		}

		md, video, err := processStep(client, html, step, folder, linkMap, cfg, used, stdout)
		if err != nil {
			return err
		}

		if video != nil && !cfg.SkipVideo {
			dest := filepath.Join(folder, titleSane+".mp4")
			if !fileNonEmpty(dest) {
				videoJobs = append(videoJobs, videoJob{video.VzaarID, dest})
			}
		}

		if err := os.WriteFile(mdPath, []byte(md), 0o644); err != nil {
			return err
		}

		client.Pace()
	}

	// ---- Phase 2: videos via ffmpeg (no cookies needed) ----
	for j, job := range videoJobs {
		fmt.Fprintf(stdout, "  [video %d/%d] %s\n", j+1, len(videoJobs), filepath.Base(job.Dest))
		client.Video(job.VzaarID, job.Dest)
	}

	// ---- ToC ----
	toc := markdown.BuildToC(rootName, items, locked, sourceURL)
	if err := os.WriteFile(filepath.Join(root, "ToC.md"), []byte(toc), 0o644); err != nil {
		return err
	}

	doneMsg := fmt.Sprintf("\nDone. Course saved under: %s", root)
	if skipped > 0 {
		doneMsg += fmt.Sprintf(" (%d step(s) already complete, skipped)", skipped)
	}
	fmt.Fprintln(stdout, doneMsg)

	if len(locked) > 0 {
		parts := make([]string, len(locked))
		for i, week := range locked {
			if week.UnlocksAt != "" {
				parts[i] = fmt.Sprintf("Week %d on %s", week.Number, week.UnlocksAt)
			} else {
				parts[i] = fmt.Sprintf("Week %d", week.Number)
			}
		}
		nxt := strings.Join(parts, ", ")
		if cfg.SkipLocked {
			fmt.Fprintf(stdout, "Still locked: %s. Re-run then to pick them up.\n", nxt)
		} else {
			fmt.Fprintf(stdout, "WARNING: included %d week(s) not yet released to you (%s).\n", len(locked), nxt)
		}
	}

	return nil
}

func fileNonEmpty(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Size() > 0
}

// courseRunRE captures "/courses/<slug>/<run>" — the identity of one course run.
var courseRunRE = regexp.MustCompile(`/courses/[A-Za-z0-9._~-]+/\d+`)

// scopedStepURLRE matches a step link belonging to one run. Scoping matters: an
// overview page also carries "course highlights" cards that live under /info/
// and point at a different run, and following one of those lands on a page with
// no tree.
func scopedStepURLRE(run string) *regexp.Regexp {
	return regexp.MustCompile(regexp.QuoteMeta(run) + `/steps/\d+`)
}

// fetchCoursePage fetches a course URL and returns HTML that carries the course
// tree. The tree lives in the course-content sidebar on enrolled pages, which
// in practice means a step page. A run's overview page does not render it, but
// it links to the run's to-do page, which does list this run's steps — so that
// is the first fallback, then a step link from whichever page we hold.
func fetchCoursePage(client Client, courseURL string, stdout io.Writer) (string, error) {
	text, err := client.Text(courseURL)
	if err != nil {
		return "", err
	}
	if course.HasCourseTree(text) {
		return text, nil
	}

	run := courseRunRE.FindString(courseURL)
	if run == "" {
		return text, nil // not a run URL, so there is nothing to scope a fallback to
	}
	stepRE := scopedStepURLRE(run)

	if loc := stepRE.FindString(text); loc != "" {
		return followStep(client, courseURL, loc, stdout)
	}

	// An overview page carries no steps of its own, so try the run's to-do page.
	todo, err := runToDoURL(courseURL, run)
	if err != nil {
		return text, nil
	}
	todoHTML, err := client.Text(todo)
	if err != nil {
		return text, nil // let RunOne raise the real error
	}
	fmt.Fprintf(stdout, "  (no course tree here; following %s)\n", todo)
	if course.HasCourseTree(todoHTML) {
		return todoHTML, nil
	}
	if loc := stepRE.FindString(todoHTML); loc != "" {
		return followStep(client, todo, loc, stdout)
	}
	return todoHTML, nil
}

// followStep fetches the step named by loc, a URL reference resolved against base.
func followStep(client Client, base, loc string, stdout io.Writer) (string, error) {
	stepURL, err := resolveAgainst(base, loc)
	if err != nil {
		return "", err
	}
	fmt.Fprintf(stdout, "  (no course tree here; following %s)\n", stepURL)
	return client.Text(stepURL)
}

// runToDoURL is a run's to-do page, derived from any URL inside that run.
func runToDoURL(courseURL, run string) (string, error) {
	u, err := url.Parse(courseURL)
	if err != nil {
		return "", err
	}
	u.Path = run + "/todo"
	u.RawQuery, u.Fragment = "", ""
	return u.String(), nil
}

func resolveAgainst(base, ref string) (string, error) {
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

// Run builds a Client from cfg, fetches cfg.CourseURL's course page, then
// calls RunOne.
func Run(cfg Config, stdout io.Writer) error {
	client, err := newClient(cfg, stdout)
	if err != nil {
		return err
	}
	treeHTML, err := fetchCoursePage(client, cfg.CourseURL, stdout)
	if err != nil {
		return err
	}
	return RunOne(treeHTML, client, cfg, stdout)
}
