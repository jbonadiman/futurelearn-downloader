# futurelearn-downloader: Go port — design

**Issue:** [#8 Promote to Go CLI](https://github.com/jbonadiman/futurelearn-downloader/issues/8)
**Status:** approved 2026-09-17
**Supersedes:** the Python tool at `src/futurelearn_downloader/` (v0.2.5), deleted in this change

## Goal

Replace the Python CLI with a Go CLI that behaves the same for every documented
flag, output path and resume rule, so the tool installs as one static binary and
no longer needs `uv`, a `.venv`, `curl_cffi` or `markdownify`.

## Decisions

| # | Decision | Choice |
|---|---|---|
| — | Language | **Go** — single static CGO-free binary, 37 s cold build, `tls-client` (22 Chrome profiles) vs Rust's BoringSSL + 5 m 40 s build and a mid-rename impersonation ecosystem |
| Q1 | Markdown parity bar | **Structure-equivalent.** Same headings/lists/links/images/tables/order; cosmetic whitespace and table padding may differ |
| Q2 | Python's fate | **Replaced in one PR.** `src/`, `pyproject.toml`, `uv.lock`, `.pytest_cache/`, `tests/` all deleted; README rewritten |
| Q3 | Cloudflare | **Bounded profile rotation on 403** (≤3 profiles, spaced by `--delay`), then today's failure and `cookies.txt` re-export message. Only behaviour delta in the port |
| Q4 | Existing trees | **Keep resuming.** Identical sanitising, layout and resume rule; a Python-made tree continues under Go with zero re-fetch |
| Q5 | Validation | **Fixtures + live e2e + real HLS playlist and segments** (captured 2026-09-17, then synthesised — see Fixtures) |
| Q6 | Fixture licensing | **Synthesised.** Repo will be public, so captured course prose, media and subtitles never ship; structure is kept real, text is placeholder |

### Why the parity bar is structure-equivalent, not byte-identical

The spike compared `markdownify(heading_style="ATX")` against
`html-to-markdown/v2` (bullet `*` + table plugin) and `htmd` on the same body
HTML. Both matched on headings, strong/em, links, images, soft breaks and code
fences. Deltas: table column padding and separator style, one whitespace-only
line where a `<blockquote>` wrapper is stripped, and a trailing newline. Closing
those means post-processing the converter output for cosmetics only, so the port
keeps `markdownify`'s *semantics* and accepts its *whitespace*.

## Global constraints

- Go floor: `go 1.24` in `go.mod`. Toolchain used to build: go 1.26.5.
- `CGO_ENABLED=0`. No cgo dependency of any kind.
- Module path: `github.com/jbonadiman/futurelearn-downloader`.
- Dependencies, exactly these:
  - `github.com/bogdanfinn/tls-client` v1.16.0 (+ `github.com/bogdanfinn/fhttp` v0.6.9)
  - `github.com/JohannesKaufmann/html-to-markdown/v2` v2.5.2 (+ `plugin/base`, `plugin/commonmark`, `plugin/table`)
- No CLI framework: stdlib `flag`. 14 flags, no subcommands.
- External processes: `ffmpeg` and `ffprobe` only, via `os/exec`, same argument
  vectors as today.
- User-facing option text, warning lines and the `Done. Course saved under:`
  line stay verbatim so existing muscle memory and any docs still apply.

## Layout

```
main.go                        flag parsing into Config, wiring, exit codes
internal/course/tree.go        tree JSON -> []Step (parts, title, href, stepNumber, type, locked)
internal/course/names.go       Sanitize, UniqueName: NFKD, translit table, Windows-reserved, -2 suffix
internal/fetch/client.go       tls-client session, cookie jar, profile rotation, --delay pacing,
                               FetchText / FetchToFile / ResolveFinalURL
internal/fetch/media.go        HLS: master -> variant -> parallel segments -> ffmpeg concat-mux
                               (.part + rename), ffmpeg fallback, subtitles, related files,
                               inline audio/images, step-link localisation
internal/markdown/convert.go   body HTML -> markdown (unescape, strip blockquote wrappers,
                               base+commonmark+table converter)
internal/markdown/step.go      BuildStepMarkdown: title/H1 dedup, blockquote rules, transcript,
                               quiz section, download list, audio players
internal/markdown/toc.go       BuildToC
internal/quiz/                 quiz introduction/questions + paginated payload fetch
internal/testdata/             captured fixtures + reference tree (see Testing)
```

One writer per step, no shared mutable state across steps: the port keeps
today's sequential step loop with a parallel segment downloader inside a single
video, exactly as the Python does.

## Data flow

1. `main` parses flags, builds the client, iterates course URLs (single URL or
   `--links FILE`).
2. Course page fetch; if no tree in the page, follow the first
   `/courses/<slug>/<run>/steps/<id>` link (today's `fetch_course_page`).
3. `course.ParseTree` -> steps in document order, locked weeks reported.
4. `--dry-run` prints `Would scrape N step(s) into: <root>` and stops.
5. Per step, sequentially: skip if complete (unless `--force`); fetch; extract;
   resolve related links; localise audio, images, step links; DNS subtitles;
   defer video jobs; write `<title>.md` **last**.
6. Phase 2: video jobs, cookie-less session, parallel segments, ffmpeg mux.
7. `ToC.md` with `source_url` frontmatter.

## Parity contract

- **Flags:** `course_url`, `--links`, `--cookies` (required), `-o/--out`,
  `--limit`, `--delay`, `--skip-video`, `--skip-subs`, `--skip-downloads`,
  `--skip-audio`, `--skip-quiz`, `--force`, `--skip-locked`, `--dry-run`.
  Mutually-exclusive and missing-argument errors keep working as today.
- **Resume:** a step is done when its `.md` exists and every local file that page
  links to exists and is non-empty. Links to *other* steps' `.md` files do not
  count toward the check.
- **Atomicity:** videos are muxed to `<name>.mp4.part` and renamed on success
  only.
- **Layout:** identical folder/file names and collision suffixes, so a tree
  written by 0.2.5 resumes without a single re-fetch.
- **Sanitising:** `TRANSLIT` table, `DROP` set, NFKD, trailing space/dot strip,
  Windows-reserved guard, `-2` collision suffix — ported value-for-value.
- **Deliberate deviation:** `--version` prints the ldflags-injected version.
  0.2.5 had no such flag; without `pyproject.toml` there is otherwise no way to
  ask a binary what it is. One line of code, veto it and it goes.

## Cloudflare handling

`tls-client` with `WithClientProfile`. The spike showed the passing profile is
library-specific and changes over time: chrome124/131/133 -> 403 on the same URL
that chrome144/152 fetched fine, while `curl_cffi` passed on chrome124 and a
profile that worked at 09:15 403'd minutes later. So:

- order: `Chrome_152`, `Chrome_144`, `Chrome_133` (spike: 152 and 144 pass, 133 is the
  fallback; the list is data in one place);
- the first profile that returns a non-403 is reused for the rest of the run;
- on 403, advance to the next profile, spaced by `--delay`, max 3 attempts;
- all 403 -> today's message: re-export `cookies.txt`.

## HLS: ported value-for-value

`HLS_WORKERS = 8` (measured sweet spot on the CDN; 12 ~equal, 16 worse).

1. Fetch `https://view.vzaar.com/{vzaarVideoId}/adaptive.m3u8`, keep the URL
   after redirects, pick the highest-`BANDWIDTH` `#EXT-X-STREAM-INF` line that is
   not `I-FRAME`, take the following line as variant URI, resolve it against the
   post-redirect master URL.
2. Fetch the variant. Reject with the exact errors when it contains
   `#EXT-X-KEY` (`encrypted segments (EXT-X-KEY) — ffmpeg handles these`), lacks
   `#EXT-X-ENDLIST` (`live playlist (no ENDLIST) — ffmpeg handles these`), or has
   no segment lines (`segment list is empty`). Segment URLs are the non-`#` lines
   resolved against the variant's post-redirect URL (the variant carries a signed
   `context` query param that segment URLs inherit).
3. `duration` = sum of all `#EXTINF:` floats in the variant.
4. Scratch dir `os.MkdirTemp(dir(dest), "flhls-")` next to the destination, so
   ~200 MB videos never land on a small `/tmp`. Segments written as
   `seg_%05d.ts`, 64 KiB read chunks, one session per worker, 8 in parallel.
5. A feeder goroutine pipes finished segments into ffmpeg stdin **in playlist
   order** (download and mux overlap), with a failure flag so a dead segment
   aborts the mux instead of hanging it:

   ```
   ffmpeg -y -loglevel error -f mpegts -i pipe:0 -c copy -bsf:a aac_adtstoasc \
          -movflags +faststart -f mp4 <dest>.part
   ```

   stderr drained on its own goroutine (never `Wait` without draining),
   `proc.Wait` bounded at 600 s.
6. Failure if exit != 0 or `<dest>.part` missing/empty.
7. ffprobe backstop before the rename — a muxed stream that went off-bounds must
   not be renamed into place:

   ```
   ffprobe -v error -show_entries format=duration -of default=nw=1:nk=1 <dest>.part
   ```

   Reject when `got <= 0` or `abs(got-duration)/duration > 0.05`, with the message
   `muxed duration %.1fs vs playlist %.1fs`.
8. `os.Rename(<dest>.part, <dest>)` only then.
9. `defer`: remove the scratch dir and any surviving `.part`.
10. Any failure in 1-9 -> `      ! native HLS download failed (%v); using ffmpeg instead`
    and the fallback, which is ffmpeg's own demuxer against the master URL with
    the same `-c copy/-bsf:a aac_adtstoasc/-movflags +faststart/-f mp4` tail,
    writing `.part` and renaming on success, else removing it and printing
    `      ! ffmpeg failed: %s`.

## Testing

Offline (always, `go test ./...`):

- `names`: sanitising table test ported from the Python behaviour (Roman numerals,
  curly quotes, `/` and `:` replacement, Windows-reserved, `-2` collisions).
- `course`: tree parse from `testdata/pages/course-tree.html.gz`, 27 steps, week
  and locked-week metadata from `testdata/manifest.json`.
- `markdown`: converter output on `testdata/pages/*.html.gz` compared
  structurally against `testdata/goldens/*.md` (normalise whitespace, then
  assert heading/list/link/image/table sequences match).
- `hls`: parse `testdata/hls/master.m3u8`, pick the same variant as the Python
  did (assert against `manifest.json` `hls_video.variant_uri`), resolve the child
  URL against the signed `context` param, then mux `testdata/hls/seg000..002.ts`
  through the production argument vector with a variant playlist trimmed to
  those 3 segments (the tolerance check compares against the playlist sum, so a
  full 19-segment playlist would fail by design), asserting ffprobe duration.
- `hls` edge cases from synthetic playlists: `#EXT-X-KEY` -> fallback, missing
  `#EXT-X-ENDLIST` -> fallback, no segment lines -> fallback, `I-FRAME`-only
  master -> fallback.
- ported from Python `tests/`: `test_build_step_markdown`,
  `test_build_toc`, `test_localize`.
- resume: point the port at `testdata/python-tree/` and assert nothing is
  fetched and no file is rewritten.

## Fixtures: synthesised, never captured content

The repo is public, so no licensed course prose ships in it. Capture is done locally
(throwaway script, `cf_clearance` window), then **synthesised**: JSON key structure,
HTML tag structure, attribute shapes and element nesting are kept verbatim from the real
pages, while every human-readable string is replaced with a deterministic placeholder.

The synthesiser replaces string *values* only — never keys, never tags — so the parser is
still tested against real FutureLearn shapes, not against our guess at them. Placeholders
deliberately carry the sanitising edge cases the port must handle: Roman numerals, curly
quotes, `:` and `/`, accents, `…`, `CON`.

| fixture | content |
|---|---|
| `pages/` | 9 synthesised step pages incl. tree page, one with images + downloads |
| `hls/` | synthetic `master.m3u8` / `variant.m3u8` in the real shape (signed `context` param kept, fake signature) + 3 self-generated `.ts` segments (ffmpeg `testsrc` + sine) |
| `goldens/` | Python 0.2.5 markdown, regenerated by running its own functions over the synthesised pages |
| `python-tree/` | reference tree in 0.2.5's layout, built from the goldens + generated placeholder assets (resume oracle) |
| `manifest.json` | source shape, 27-step tree, per-page video/download flags, variant URI |
| `README.md` | provenance, the capture+synthesis procedure, and the rule that no captured content is ever committed |

No video frames, audio samples, PDFs, PNGs or subtitles from the course are committed;
the only binary media in the repo are segments we generate ourselves.


Manual, once, recorded in the PR: point both the 0.2.5 Python tool and the Go
build at the same course (`--limit 5 --skip-video`) and diff the two trees.

## Risks and unverified paths

- **No quiz fixture.** This course has no quiz/test steps (types are Article,
  Video, Discussion only). The quiz code path ports from source but ships
  fixture-less; it is the least-verified area of the port.
- **No paginated payload fixture.** `payloads/` is empty for the same reason.
- **CF profile drift.** A `tls-client` update can rename or resequence profiles;
  the rotation list is data, keep it one place.
- **`cf_clearance` is IP- and time-bound.** Live e2e must run inside the export's
  window.

## Out of scope

No new feature beyond `--version`. No resume-marker change, no manifest file, no
config file, no retry/backoff beyond the 403 rotation, no restructured output
layout, no parallel *steps* (only parallel segments within a video).

## Migration

One PR: Go sources + fixtures + tests added; `src/`, `tests/`, `pyproject.toml`,
`uv.lock`, `.pytest_cache/` deleted; README rewritten for
`go install github.com/jbonadiman/futurelearn-downloader@latest` and the
`--version` flag; `.gitignore` reduced to secrets and Go build artifacts.
Tag `v0.3.0`.
