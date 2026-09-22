package fetch

import (
	"fmt"
	"html"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	fhttp "github.com/bogdanfinn/fhttp"

	"github.com/jbonadiman/futurelearn-downloader/internal/course"
)

// audioURLRE matches inline audio in body/question HTML, either a plain
// <a href="…mp3"> or a <span class="soundcite" data-url="…mp3"> player
// wrapper (the Markdown converter drops the span, so the URL is rewritten
// in the HTML *before* conversion and both attributes follow).
var audioURLRE = regexp.MustCompile(`(?i)https?://[^\s"'<>)\]]+\.(?:mp3|m4a|wav|ogg|aac)(?:\?[^\s"'<>)\]]*)?`)

// extMap maps relatedFiles[].type -> file extension, falling back to the
// final URL's extension.
var extMap = map[string]string{
	"pdf": ".pdf", "audio": ".mp3", "video": ".mp4", "image": ".png",
	"doc": ".doc", "docx": ".docx", "xls": ".xls", "xlsx": ".xlsx",
	"ppt": ".ppt", "pptx": ".pptx", "zip": ".zip", "txt": ".txt",
}

var imageExtRE = regexp.MustCompile(`(?i)\.(?:png|jpe?g|gif|webp|bmp|svg|avif|ico)$`)

// imageExt returns the image extension (with dot) when url points at an
// image, else "".
func imageExt(rawURL string) string {
	u, err := url.Parse(rawURL)
	path := rawURL
	if err == nil {
		path = u.Path
	}
	return imageExtRE.FindString(path)
}

// findQuotedAttr returns every attr value on tagPrefix elements, in
// document order. Manually matches the opening/closing quote since Go's
// RE2 has no backreferences for a `(["'])(.*?)\1`-style pattern.
func findQuotedAttr(html, tagPrefix, attr string) []string {
	tagRE := regexp.MustCompile(`(?is)<` + tagPrefix + `\b[^>]*?\b` + attr + `\s*=\s*(["'])`)
	var out []string
	for _, loc := range tagRE.FindAllStringSubmatchIndex(html, -1) {
		quote := html[loc[2]:loc[3]]
		rest := html[loc[1]:]
		end := strings.Index(rest, quote)
		if end < 0 {
			continue
		}
		out = append(out, rest[:end])
	}
	return out
}

// RelatedFile downloads a related file, deriving its extension from ftype
// (extMap) or — when ftype is unknown — the final URL after following
// redirects. Skips the transfer entirely when the file is already on disk.
func (c *Client) RelatedFile(linkURL, ftype, titleSane, folder string, used map[string]bool) (string, error) {
	ext, resp, err := c.relatedFileExtension(linkURL, ftype)
	if err != nil {
		return "", err
	}

	fname := course.UniqueName(titleSane, ext, used)
	dest := filepath.Join(folder, fname)
	if fileNonEmpty(dest) {
		if resp != nil {
			resp.Body.Close()
		}
		return fname, nil
	}

	if resp == nil {
		resp, err = c.get(linkURL)
		if err != nil {
			return "", err
		}
	}
	defer resp.Body.Close()
	if _, err := writeResponseBody(resp, dest); err != nil {
		return "", err
	}
	return fname, nil
}

// relatedFileExtension resolves the extension for RelatedFile. When ftype
// is known, no request is made at all. Otherwise it makes exactly one GET
// and returns the still-open response so RelatedFile can reuse its body
// instead of re-fetching.
func (c *Client) relatedFileExtension(linkURL, ftype string) (ext string, resp *fhttp.Response, err error) {
	if e, ok := extMap[ftype]; ok {
		return e, nil, nil
	}
	resp, err = c.get(linkURL)
	if err != nil {
		return "", nil, err
	}
	final := linkURL
	if resp.Request != nil && resp.Request.URL != nil {
		final = resp.Request.URL.String()
	}
	ext = filepath.Ext(strings.SplitN(final, "?", 2)[0])
	if ext == "" {
		ext = ".bin"
	}
	return ext, resp, nil
}

// LocalizeAudio downloads every inline audio clip in bodyHTML and
// rewrites its URL to the local filename, returning (html, filenames) in
// document order. A clip whose download fails is dropped entirely (no
// orphan link, no reserved name).
func (c *Client) LocalizeAudio(bodyHTML, folder, base string, used map[string]bool) (string, []string, error) {
	seen := map[string]bool{}
	var urls []string
	for _, raw := range audioURLRE.FindAllString(bodyHTML, -1) {
		if !seen[raw] {
			seen[raw] = true
			urls = append(urls, raw)
		}
	}

	var names []string
	for _, raw := range urls {
		u := html.UnescapeString(raw)
		ext := strings.ToLower(filepath.Ext(strings.SplitN(u, "?", 2)[0]))
		if ext == "" {
			ext = ".mp3"
		}
		n := len(names) + 1
		fname := fmt.Sprintf("%s-audio-%d%s", base, n, ext)
		for k := 2; used[fname]; k++ {
			fname = fmt.Sprintf("%s-audio-%d-%d%s", base, n, k, ext)
		}

		dest := filepath.Join(folder, fname)
		if !fileNonEmpty(dest) {
			if _, err := c.ToFile(u, dest); err != nil {
				fmt.Fprintf(c.log, "      ! audio failed: %v\n", err)
				continue
			}
			fmt.Fprintf(c.log, "      audio -> %s\n", fname)
		}
		used[fname] = true
		names = append(names, fname)
		bodyHTML = strings.ReplaceAll(bodyHTML, raw, fname)
	}
	return bodyHTML, names, nil
}

// LocalizeImages downloads every inline <img> and image-link target in
// bodyHTML and rewrites it to the local filename. Unlike LocalizeAudio,
// the name is reserved (added to used) before the download attempt, so a
// failed download still consumes a name slot but leaves the remote URL
// unreplaced.
func (c *Client) LocalizeImages(bodyHTML, folder, base string, used map[string]bool) (string, error) {
	var raws []string
	seen := map[string]bool{}
	for _, u := range findQuotedAttr(bodyHTML, "img", "src") {
		u = strings.TrimSpace(u)
		if u != "" && !seen[u] {
			seen[u] = true
			raws = append(raws, u)
		}
	}
	for _, u := range findQuotedAttr(bodyHTML, "a", "href") {
		u = strings.TrimSpace(u)
		if u != "" && !seen[u] && imageExt(u) != "" {
			seen[u] = true
			raws = append(raws, u)
		}
	}

	for _, raw := range raws {
		u := html.UnescapeString(raw)
		ext := imageExt(u)
		if ext == "" {
			ext = ".png"
		}
		stem := urlStem(u)
		if stem != "" {
			stem = course.Sanitize(stem)
		} else {
			stem = base + "-image"
		}
		fname := course.UniqueName(stem, ext, used)

		dest := filepath.Join(folder, fname)
		if !fileNonEmpty(dest) {
			if _, err := c.ToFile(u, dest); err != nil {
				fmt.Fprintf(c.log, "      ! image failed: %v\n", err)
				continue
			}
			fmt.Fprintf(c.log, "      image -> %s\n", fname)
		}
		bodyHTML = strings.ReplaceAll(bodyHTML, raw, fname)
	}
	return bodyHTML, nil
}

// fileExtRE matches the extensions backed up when they appear as a plain
// link in a step's body. Images and audio have their own localisers;
// these are the document types courses link out to.
var fileExtRE = regexp.MustCompile(`(?i)\.(?:pdf|docx?|xlsx?|pptx?|csv|rtf|odt|ods|odp|epub|zip)$`)

// LocalizeFiles downloads every body link that points at a document and
// rewrites it to the local filename. Courses that host their handouts
// off-site put them in the prose rather than in relatedFiles, so without
// this the reader has to fetch them by hand. A failed download leaves the
// remote URL in place; a name is only taken via RelatedFile once the
// download succeeds.
func (c *Client) LocalizeFiles(bodyHTML, folder, base string, used map[string]bool) (string, []string, error) {
	var raws []string
	seen := map[string]bool{}
	for _, u := range findQuotedAttr(bodyHTML, "a", "href") {
		u = strings.TrimSpace(u)
		if u == "" || seen[u] || fileExtRE.FindString(urlPathOf(u)) == "" {
			continue
		}
		seen[u] = true
		raws = append(raws, u)
	}

	var names []string
	for _, raw := range raws {
		u := html.UnescapeString(raw)
		if strings.HasPrefix(u, "/") {
			u = course.BaseURL + u
		}
		stem := urlStem(u)
		if stem == "" {
			stem = base + "-file"
		} else {
			stem = course.Sanitize(stem)
		}

		fname, err := c.RelatedFile(u, "", stem, folder, used)
		if err != nil {
			fmt.Fprintf(c.log, "      ! file failed: %v\n", err)
			continue
		}
		fmt.Fprintf(c.log, "      file -> %s\n", fname)
		names = append(names, fname)
		bodyHTML = strings.ReplaceAll(bodyHTML, raw, fname)
	}
	return bodyHTML, names, nil
}

func urlPathOf(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	return u.Path
}

// urlStem returns the filename stem of rawURL's path — no directory, no
// extension — or "" when the URL carries no filename to name a file after.
func urlStem(rawURL string) string {
	path := urlPathOf(rawURL)
	return strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
}

// Subtitles fetches every video.subtitles entry, writing trimmed VTT text
// to disk. A fetch failure is logged and skipped, but — unlike
// LocalizeAudio — the filename is still returned, since the Markdown
// links to it either way.
func (c *Client) Subtitles(video *course.Video, folder, base string) ([]string, error) {
	if video == nil {
		return nil, nil
	}
	seen := map[string]int{}
	var names []string
	for _, sub := range video.Subtitles {
		if sub.Src == "" {
			continue
		}
		lang := strings.ToLower(firstNonEmpty(sub.SrcLang, sub.Label, "sub"))
		index := seen[lang]
		seen[lang] = index + 1

		fname := course.SubtitleBasename(base, sub.SrcLang, sub.Label, index)
		names = append(names, fname)

		dest := filepath.Join(folder, fname)
		if fileNonEmpty(dest) {
			continue
		}
		text, err := c.Text(sub.Src)
		if err != nil {
			fmt.Fprintf(c.log, "      ! subtitle failed: %v\n", err)
			continue
		}
		if err := writeTrimmedText(dest, text); err != nil {
			return names, err
		}
	}
	return names, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func writeTrimmedText(dest, text string) error {
	return os.WriteFile(dest, []byte(strings.TrimSpace(text)+"\n"), 0o644)
}

// ResolveRelatedLinks returns links with every URL rewritten to its final
// redirect target.
func (c *Client) ResolveRelatedLinks(links []course.RelatedLink) []course.RelatedLink {
	out := make([]course.RelatedLink, len(links))
	for i, lnk := range links {
		target := lnk.URL
		title := lnk.Title
		if title == "" {
			title = firstNonEmpty(target, "link")
		}
		if strings.HasPrefix(target, "/") {
			target = course.BaseURL + target
		}
		out[i] = course.RelatedLink{Title: title, URL: c.FinalURL(target)}
	}
	return out
}
