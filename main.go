// Command futurelearn-downloader scrapes one or more FutureLearn courses to folders
// + Markdown.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"strings"
	"time"

	"github.com/jbonadiman/futurelearn-downloader/internal/scrape"
)

// version is overridden at build time: -ldflags "-X main.version=$(git describe --tags --always)".
var version = "dev"

// buildVersion reports the linker-injected version, falling back to the
// module version that go install records, so a binary built straight from
// the module proxy does not claim to be "dev".
func buildVersion() string {
	if version != "dev" {
		return version
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	return version
}

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		// --help is a successful outcome, not a failure to report.
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(0)
		}
		fmt.Fprintln(os.Stderr, "!", err)
		os.Exit(1)
	}
}

// valueFlags are the flags stdlib flag needs a following token for, so
// splitPositional can tell a flag's value apart from the course URL.
var valueFlags = map[string]bool{
	"cookies": true, "links": true, "o": true, "out": true,
	"limit": true, "delay": true,
}

// splitPositional pulls the optional course-URL positional out of args so
// it can appear anywhere relative to flags — stdlib flag.Parse stops at
// the first non-flag token and treats everything after it as positional,
// which would otherwise silently ignore any flag placed after the URL.
func splitPositional(args []string) (rest []string, positional string, err error) {
	var found []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") {
			rest = append(rest, a)
			name := strings.TrimLeft(a, "-")
			if !strings.Contains(name, "=") && valueFlags[name] && i+1 < len(args) {
				i++
				rest = append(rest, args[i])
			}
			continue
		}
		found = append(found, a)
	}
	if len(found) > 1 {
		return nil, "", fmt.Errorf("unexpected extra argument(s): %v", found[1:])
	}
	if len(found) == 1 {
		positional = found[0]
	}
	return rest, positional, nil
}

func run(args []string, stdout io.Writer) error {
	rest, courseURL, err := splitPositional(args)
	if err != nil {
		return err
	}

	fs := flag.NewFlagSet("futurelearn-downloader", flag.ContinueOnError)
	fs.SetOutput(stdout)
	fs.Usage = func() {
		fmt.Fprintln(stdout, "Scrape one or more FutureLearn courses to folders + markdown.")
		fmt.Fprintln(stdout, "\nUsage: futurelearn-downloader [flags] course_url")
		fmt.Fprintln(stdout, "   or: futurelearn-downloader [flags] --links FILE")
		fs.PrintDefaults()
	}

	var cfg scrape.Config
	var showVersion bool
	var linksPath, out string
	var delaySeconds float64

	fs.BoolVar(&showVersion, "version", false, "print version and exit")
	fs.StringVar(&linksPath, "links", "", "file with one course URL per line; downloads each (blank lines and # comments ignored)")
	fs.StringVar(&cfg.CookiesPath, "cookies", "", "path to a Netscape-format cookies.txt (required)")
	fs.StringVar(&out, "o", ".", "output directory")
	fs.StringVar(&out, "out", ".", "output directory")
	fs.IntVar(&cfg.Limit, "limit", 0, "process first N steps (0=all)")
	fs.Float64Var(&delaySeconds, "delay", 0.3, "pause between step pages, in seconds")
	fs.BoolVar(&cfg.SkipVideo, "skip-video", false, "don't download videos")
	fs.BoolVar(&cfg.SkipSubs, "skip-subs", false, "don't download video subtitles")
	fs.BoolVar(&cfg.SkipDownloads, "skip-downloads", false, "don't download files linked from a step")
	fs.BoolVar(&cfg.SkipAudio, "skip-audio", false, "don't localise inline audio clips")
	fs.BoolVar(&cfg.SkipQuiz, "skip-quiz", false, "don't scrape quiz/test questions")
	fs.BoolVar(&cfg.Force, "force", false, "re-scrape steps that are already complete (default: skip them)")
	fs.BoolVar(&cfg.SkipLocked, "skip-locked", false, "take only released weeks (default: scrape locked ones too)")
	fs.BoolVar(&cfg.DryRun, "dry-run", false, "print the plan, do nothing")

	if err := fs.Parse(rest); err != nil {
		return err
	}

	if showVersion {
		fmt.Fprintln(stdout, buildVersion())
		return nil
	}

	if cfg.CookiesPath == "" {
		return fmt.Errorf("--cookies is required")
	}
	if linksPath != "" && courseURL != "" {
		return fmt.Errorf("give either a course URL or --links FILE, not both")
	}
	if linksPath == "" && courseURL == "" {
		return fmt.Errorf("provide a course URL, or --links FILE with one course URL per line")
	}

	cfg.Out = out
	cfg.Delay = time.Duration(delaySeconds * float64(time.Second))

	if linksPath != "" {
		urls, err := readLinks(linksPath)
		if err != nil {
			return err
		}
		if len(urls) == 0 {
			return fmt.Errorf("no course URLs found in %s", linksPath)
		}
		for _, u := range urls {
			fmt.Fprintf(stdout, "\n=== %s ===\n", u)
			cfg.CourseURL = u
			if err := scrape.Run(cfg, stdout); err != nil {
				fmt.Fprintf(stdout, "! %s: %v\n", u, err)
			}
		}
		return nil
	}

	fmt.Fprintf(stdout, "\n=== %s ===\n", courseURL)
	cfg.CourseURL = courseURL
	return scrape.Run(cfg, stdout)
}

// readLinks reads one course URL per line, skipping blank lines and #
// comments, dropping exact duplicates while preserving first-seen order.
func readLinks(path string) ([]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var urls []string
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !seen[line] {
			seen[line] = true
			urls = append(urls, line)
		}
	}
	return urls, nil
}
