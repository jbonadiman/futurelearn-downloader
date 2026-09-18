package markdown

import (
	"fmt"
	"html"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jbonadiman/futurelearn-downloader/internal/course"
)

// DownloadLink is a related file already fetched locally: the label shown in
// the "## Downloads" section and the filename it was saved under.
type DownloadLink struct {
	Label    string
	Filename string
}

// StepInput is everything BuildStepMarkdown needs to render one step page.
type StepInput struct {
	Title         string
	TitleSane     string
	BodyHTML      string
	Copyright     string
	Video         *course.Video
	RelatedLinks  []course.RelatedLink
	SubLinks      []string
	DownloadLinks []DownloadLink
	AudioFiles    []string
	QuizLines     []string
}

var (
	sentenceBoundaryRE = regexp.MustCompile(`[.!?]\s+[A-Z0-9"“]`)
	titleQuoteRE       = regexp.MustCompile(`["'“”‘’《》「」『』]`)
	titleTrailingRE    = regexp.MustCompile(`[\s?.!:]+$`)
	stripTagsRE        = regexp.MustCompile(`<[^>]+>`)
)

// leadIsShort reports whether the lead reads as a subtitle (one short
// sentence) rather than an overview.
func leadIsShort(lead string) bool {
	text := strings.TrimSpace(lead)
	if utf8.RuneCountInString(text) > 140 {
		return false
	}
	return !sentenceBoundaryRE.MatchString(text)
}

func blockquote(text string) string {
	return "> " + strings.ReplaceAll(text, "\n", "\n> ")
}

func normalizeHeading(text string) string {
	text = titleQuoteRE.ReplaceAllString(text, "")
	text = strings.TrimSpace(text)
	text = titleTrailingRE.ReplaceAllString(text, "")
	return strings.ToLower(text)
}

// isRedundantH1 reports whether the body's own opening H1 just repeats
// the step title.
func isRedundantH1(lead, title string) bool {
	if !strings.HasPrefix(lead, "# ") {
		return false
	}
	heading := normalizeHeading(lead[2:])
	stepTitle := normalizeHeading(title)
	if heading == "" || stepTitle == "" {
		return false
	}
	return heading == stepTitle || strings.HasPrefix(heading, stepTitle) || strings.HasPrefix(stepTitle, heading)
}

// BuildStepMarkdown renders one step page as Markdown.
func BuildStepMarkdown(in StepInput) string {
	lines := []string{"# " + in.Title, ""}

	if in.Video != nil {
		lines = append(lines, fmt.Sprintf(`<video controls src="%s"></video>`, URLPath(in.TitleSane+".mp4")), "")
	}

	if in.BodyHTML != "" {
		bodyMD := BodyToMarkdown(in.BodyHTML, in.AudioFiles)
		// The first paragraph is FutureLearn's step overview: promote it to a
		// subtitle (##) only when it's a genuinely short one-liner; a longer
		// multi-sentence summary reads wrong as a heading, so render it as a
		// blockquote instead. A paragraph carrying an audio player, or a body
		// that already opens with a heading (e.g. a poll's H2), is left alone —
		// unless that heading just repeats the title we already printed above.
		lead, rest, found := strings.Cut(bodyMD, "\n\n")
		switch {
		case found && !strings.Contains(lead, "<audio"):
			lead = strings.TrimSpace(lead)
			switch {
			case isRedundantH1(lead, in.Title):
				lines = append(lines, strings.TrimSpace(rest))
			case strings.HasPrefix(lead, "#"):
				lines = append(lines, bodyMD)
			case leadIsShort(lead):
				lines = append(lines, "## "+lead, "")
				if strings.TrimSpace(rest) != "" {
					lines = append(lines, strings.TrimSpace(rest))
				}
			default:
				lines = append(lines, blockquote(lead), "")
				if strings.TrimSpace(rest) != "" {
					lines = append(lines, strings.TrimSpace(rest))
				}
			}
		default:
			lines = append(lines, bodyMD)
		}
		lines = append(lines, "")
	}

	if len(in.QuizLines) > 0 {
		lines = append(lines, in.QuizLines...)
	}

	if len(in.DownloadLinks) > 0 {
		lines = append(lines, "## Downloads", "")
		for _, d := range in.DownloadLinks {
			lines = append(lines, fmt.Sprintf("- [%s](%s)", d.Label, URLPath(d.Filename)))
		}
		lines = append(lines, "")
	}

	if len(in.RelatedLinks) > 0 {
		lines = append(lines, "## Related links", "")
		for _, l := range in.RelatedLinks {
			label := l.Title
			if label == "" {
				label = l.URL
			}
			if label == "" {
				label = "link"
			}
			lines = append(lines, fmt.Sprintf("- [%s](%s)", label, l.URL))
		}
		lines = append(lines, "")
	}

	if len(in.SubLinks) > 0 {
		lines = append(lines, "## Subtitles", "")
		for _, name := range in.SubLinks {
			lines = append(lines, fmt.Sprintf("- [%s](%s)", name, URLPath(name)))
		}
		lines = append(lines, "")
	}

	if in.Video != nil {
		if transcript := course.TranscriptText(in.Video); transcript != "" {
			lines = append(lines, "## Transcript", "", transcript, "")
		}
	}

	if in.Copyright != "" {
		copyTxt := strings.TrimSpace(stripTagsRE.ReplaceAllString(html.UnescapeString(in.Copyright), ""))
		if copyTxt != "" {
			lines = append(lines, "---", "", copyTxt, "")
		}
	}

	return strings.TrimRightFunc(strings.Join(lines, "\n"), unicode.IsSpace) + "\n"
}
