package course

import (
	"encoding/json"
	"html"
	"regexp"
	"strings"
)

// StepData is a step page's rendered content — body, copyright, downloads,
// related links, and video — every rendering and localisation task consumes.
type StepData struct {
	BodyHTML     string
	Copyright    string
	RelatedFiles []RelatedFile
	RelatedLinks []RelatedLink
	Video        *Video
}

// RelatedFile mirrors one entry of relatedContent.relatedFiles.
type RelatedFile struct {
	Type  string `json:"type"`
	URL   string `json:"url"`
	Title string `json:"title"`
}

// RelatedLink mirrors one entry of relatedContent.relatedLinks.
type RelatedLink struct {
	Title string `json:"title"`
	URL   string `json:"url"`
}

// Video mirrors videoPlayerProps.video.
type Video struct {
	VzaarID        string
	Subtitles      []Subtitle
	TranscriptHTML []string // englishHtmlTranscript.paragraphs[].text.__html, in order
}

// Subtitle mirrors one entry of video.subtitles.
type Subtitle struct {
	SrcLang string `json:"srcLang"`
	Label   string `json:"label"`
	Src     string `json:"src"`
}

var stripTagsRE = regexp.MustCompile(`<[^>]+>`)

// StripTags unescapes HTML entities, drops tags and trims the result.
func StripTags(s string) string {
	return strings.TrimSpace(stripTagsRE.ReplaceAllString(html.UnescapeString(s), ""))
}

// TranscriptText strips tags from each transcript paragraph and joins them
// with a blank line, the shape both markdown.BuildStepMarkdown's
// "## Transcript" section and quiz.Question.Markdown need.
func TranscriptText(video *Video) string {
	if video == nil {
		return ""
	}
	var out []string
	for _, raw := range video.TranscriptHTML {
		if t := StripTags(raw); t != "" {
			out = append(out, t)
		}
	}
	return strings.Join(out, "\n\n")
}

type rawHTML struct {
	HTML string `json:"__html"`
}

type rawStepContent struct {
	Body      *rawHTML `json:"body"`
	Copyright *rawHTML `json:"copyright"`
}

type rawRelatedContent struct {
	RelatedFiles []RelatedFile `json:"relatedFiles"`
	RelatedLinks []RelatedLink `json:"relatedLinks"`
}

// numericString unmarshals a JSON string or bare number into a string,
// since videoPlayerProps.video.vzaarVideoId is carried as a JSON number.
type numericString string

func (n *numericString) UnmarshalJSON(b []byte) error {
	*n = numericString(strings.Trim(string(b), `"`))
	return nil
}

type rawVideo struct {
	VzaarVideoID          numericString `json:"vzaarVideoId"`
	Subtitles             []Subtitle    `json:"subtitles"`
	EnglishHTMLTranscript *struct {
		Paragraphs []struct {
			Text rawHTML `json:"text"`
		} `json:"paragraphs"`
	} `json:"englishHtmlTranscript"`
}

type stepBlob struct {
	StepContent      *rawStepContent    `json:"stepContent"`
	RelatedContent   *rawRelatedContent `json:"relatedContent"`
	VideoPlayerProps *struct {
		Video *rawVideo `json:"video"`
	} `json:"videoPlayerProps"`
	BaseStep *struct {
		StepContent    *rawStepContent    `json:"stepContent"`
		RelatedContent *rawRelatedContent `json:"relatedContent"`
	} `json:"baseStep"`
}

// ExtractStep scans every script blob (same shape ExtractWeeks reads) for a
// step's rendered content. Body/copyright and video take the first blob
// that populates them; relatedContent takes the last blob where the key is
// present at all (an absent key must not clear an already-found list with
// an empty one).
func ExtractStep(html string) (StepData, error) {
	var data StepData
	for _, raw := range ScriptBlobs(html) {
		var blob stepBlob
		if err := json.Unmarshal([]byte(raw), &blob); err != nil {
			continue
		}

		sc := blob.StepContent
		if sc == nil && blob.BaseStep != nil {
			sc = blob.BaseStep.StepContent
		}
		if data.BodyHTML == "" && sc != nil && sc.Body != nil && sc.Body.HTML != "" {
			data.BodyHTML = sc.Body.HTML
			if sc.Copyright != nil {
				data.Copyright = sc.Copyright.HTML
			}
		}

		rc := blob.RelatedContent
		if rc == nil && blob.BaseStep != nil {
			rc = blob.BaseStep.RelatedContent
		}
		if rc != nil {
			data.RelatedFiles = rc.RelatedFiles
			data.RelatedLinks = rc.RelatedLinks
		}

		if data.Video == nil && blob.VideoPlayerProps != nil && blob.VideoPlayerProps.Video != nil {
			v := blob.VideoPlayerProps.Video
			if v.VzaarVideoID != "" {
				var transcript []string
				if v.EnglishHTMLTranscript != nil {
					for _, paragraph := range v.EnglishHTMLTranscript.Paragraphs {
						transcript = append(transcript, paragraph.Text.HTML)
					}
				}
				data.Video = &Video{
					VzaarID:        string(v.VzaarVideoID),
					Subtitles:      v.Subtitles,
					TranscriptHTML: transcript,
				}
			}
		}
	}
	return data, nil
}
