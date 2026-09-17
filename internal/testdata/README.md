# Test fixtures

Everything here is **synthesised**, never captured content. The repo is public, so no
FutureLearn course prose, title, image, PDF, subtitle or video frame from a real
enrolment is committed.

## Provenance

Captured 2026-09-17 from one enrolled course with a local throwaway script, then
synthesised:

- JSON keys, HTML tag structure, attribute shapes and nesting: **verbatim** from the
  capture, so the parser is tested against real FutureLearn shapes rather than a guess.
- Every human-readable string: a deterministic placeholder, carrying the awkward cases
  on purpose (Roman numerals, curly quotes, `:` and `/`, accents, `…`, `CON`, trailing
  spaces).
- CDN URLs: replaced by a hash path on `cdn.example.test`, keeping the extension the
  port infers file types from.
- Real video ids: remapped to `1000000+n`.

A phrase-level leak check ran over the result: no captured title and no captured
sentence of course text appears in these files (see `manifest.json`).

## Contents

| path | what it is |
|---|---|
| `pages/` | 9 synthesised pages: the tree page, a to-do page, 7 step pages |
| `media/` | `master.m3u8` + `variant.m3u8` in the real shape (signed `context` param, fake signature), 3 `.ts` segments and the subtitle/PDF/PNG/MP3 bytes — all generated with ffmpeg or by hand |
| `goldens/` | markdown produced by Python 0.2.5 over the synthesised pages |
| `python-tree/` | the full reference tree in 0.2.5's layout — the resume oracle |
| `manifest.json` | tree shape, per-page flags, media inventory, provenance |

## Refreshing

The capture needs a live `cookies.txt` (a `cf_clearance` cookie expires in ~30 minutes),
and the capture + synthesis + oracle scripts are throwaway: they were run once and are
not committed. Re-deriving them is the work of an afternoon; see the design spec at
`docs/superpowers/specs/2026-09-17-go-port-design.md` for the rules they must follow —
structure verbatim, prose replaced, and a leak check before anything is committed.
