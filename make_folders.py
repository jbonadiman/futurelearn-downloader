#!/usr/bin/env python3
# futurelearn-downloader — download a FutureLearn course to a local folder tree
# Copyright (C) 2026 greenteafox
#
# This program is free software: you can redistribute it and/or modify it under
# the terms of the GNU Affero General Public License as published by the Free
# Software Foundation, either version 3 of the License, or (at your option) any
# later version.
#
# This program is distributed in the hope that it will be useful, but WITHOUT ANY
# WARRANTY; without even the implied warranty of MERCHANTABILITY or FITNESS FOR A
# PARTICULAR PURPOSE. See the GNU Affero General Public License for more details.
#
# You should have received a copy of the GNU Affero General Public License along
# with this program. If not, see <https://www.gnu.org/licenses/>.
"""
Build a FutureLearn course folder tree, then download each lesson's video + subtitles
into the right folder with the lesson's name.

The course outline (weeks -> activities -> steps) lives in an embedded JSON blob inside
a saved course page. The video ID (vzaarVideoId) and subtitle (.vtt) URLs, however, only
exist on each individual step page, which requires an authenticated session.

Pipeline
--------
1. Parse the saved HTML -> build the folder tree (safe names, Windows+Linux friendly).
2. For every video step, fetch its step page (using your cookies) to get the video ID
   and subtitle list.
3. Download the HLS stream (.m3u8 -> .mp4) with ffmpeg, and each .vtt subtitle.

Requirements
------------
- ffmpeg on PATH (for the HLS -> mp4 remux).
- A Netscape-format cookies.txt for futurelearn.com, exported from your logged-in browser
  (e.g. the "Get cookies.txt LOCALLY" / "cookies.txt" browser extension). The file should
  include cookies for BOTH futurelearn.com and ugc.futurelearn.com (subtitle CDN).

Usage
-----
  # preview only (tree + which videos would be downloaded):
  python3 make_folders.py page.html --dry-run

  # build tree + download everything:
  python3 make_folders.py page.html --download --cookies cookies.txt -o /mnt/hdd/media/courses

  # just the folder tree, no downloads:
  python3 make_folders.py page.html
"""

import argparse
import glob
import html
import http.cookiejar
import json
import os
import re
import subprocess
import sys
import time
import unicodedata
import urllib.request

BASE_URL = "https://www.futurelearn.com"
UA = ("Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 "
      "(KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36")

SCRIPT_RE = re.compile(
    r'<script type="application/json"[^>]*>\s*<!--(.*?)-->\s*</script>', re.S)

# ---------------------------------------------------------------------------
# Name sanitising
# ---------------------------------------------------------------------------

# Explicit char -> replacement (things NFKD won't fully handle).
TRANSLIT = {
    "\u2018": "'", "\u2019": "'", "\u201A": "'", "\u201B": "'",   # curly apostrophes
    "\u201C": "", "\u201D": "", "\u201E": "", "\u201F": "",        # curly double quotes
    "\u2013": "-", "\u2014": "-", "\u2015": "-",                   # dashes
    "\u2026": "",                                                   # ellipsis
    "\u00A0": " ",                                                  # non-breaking space
    ":": " - ",                                                     # colon
    "/": "-", "\\": "-",                                            # path separators -> hyphen
    "\u2160": "1", "\u2161": "2", "\u2162": "3", "\u2163": "4",    # Roman numerals
    "\u2164": "5", "\u2165": "6", "\u2166": "7", "\u2167": "8",
    "\u2168": "9", "\u2169": "10",
}

DROP = set('<>"|?*')     # illegal on Windows / otherwise problematic

WINDOWS_RESERVED = {"CON", "PRN", "AUX", "NUL"} | \
    {f"COM{i}" for i in range(1, 10)} | \
    {f"LPT{i}" for i in range(1, 10)}


def sanitize(name: str, max_len: int = 120) -> str:
    name = html.unescape(name)
    name = unicodedata.normalize("NFKD", name)      # ü -> u, ligatures -> letters
    out = []
    for ch in name:
        if ch in TRANSLIT:
            out.append(TRANSLIT[ch])
        elif ch in DROP:
            continue
        elif unicodedata.combining(ch):             # drop accent/diaeresis marks
            continue
        elif unicodedata.category(ch).startswith("C"):
            continue
        else:
            out.append(ch)
    name = "".join(out)
    name = re.sub(r"\s+", " ", name)
    name = re.sub(r"\.{2,}", ".", name)
    name = name.strip(" .")
    if name.upper().split(".")[0] in WINDOWS_RESERVED:
        name = "_" + name
    return name[:max_len].rstrip(" .") or "untitled"


# ---------------------------------------------------------------------------
# HTML -> course tree
# ---------------------------------------------------------------------------

def extract_weeks(html_text: str):
    m = re.search(
        r'<script type="application/json"[^>]*'
        r'data-hypernova-key="componentsApplicationcomponentsCourseContentNavSidebar"[^>]*>'
        r'\s*<!--(.*?)-->\s*</script>',
        html_text, re.S,
    )
    if not m:
        for sm in SCRIPT_RE.finditer(html_text):
            try:
                data = json.loads(sm.group(1))
            except json.JSONDecodeError:
                continue
            if "courseContent" in data and "weeks" in data["courseContent"]:
                m = sm
                break
        else:
            raise SystemExit("Could not find a course structure in this HTML file.")
    return json.loads(m.group(1))["courseContent"]["weeks"]


def extract_run_title(html_text: str) -> str:
    m = re.search(r'"runTitle":"(.*?)"', html_text)
    return m.group(1) if m else "course"


def collect_steps(weeks):
    """Flatten weeks -> activities -> steps into (folder_parts, step) tuples."""
    items = []
    for week in weeks:
        wnum = week.get("number", "")
        wtitle = sanitize(week.get("title", ""))
        week_name = f"Week {wnum} - {wtitle}" if wtitle else f"Week {wnum}"
        for ai, activity in enumerate(week.get("activities", []), start=1):
            atitle = sanitize(activity.get("title", ""))
            act_name = f"{ai}. {atitle}" if atitle else f"{ai}."
            for step in activity.get("steps", []):
                snum = step.get("stepNumber", "").strip()
                stitle = sanitize(step.get("title", ""))
                step_name = f"{snum} {stitle}".strip() if snum else stitle
                items.append(([week_name, act_name, step_name], step))
    return items


# ---------------------------------------------------------------------------
# Step page -> video info
# ---------------------------------------------------------------------------

def extract_video_info(html_text: str):
    """Return {vzaarVideoId, subtitles, transcript, duration} or None."""
    for sm in SCRIPT_RE.finditer(html_text):
        try:
            data = json.loads(sm.group(1))
        except json.JSONDecodeError:
            continue
        vp = data.get("videoPlayerProps") or {}
        video = vp.get("video") or {}
        vid = video.get("vzaarVideoId")
        if vid:
            return {
                "vzaarVideoId": vid,
                "subtitles": video.get("subtitles") or [],
                "transcript": (video.get("englishHtmlTranscript") or {}).get("paragraphs") or [],
                "duration": video.get("duration"),
            }
    return None


# ---------------------------------------------------------------------------
# HTTP
# ---------------------------------------------------------------------------

def make_opener(cookie_file=None):
    if cookie_file:
        cj = http.cookiejar.MozillaCookieJar(cookie_file)
        cj.load(ignore_discard=True, ignore_expires=True)
        opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(cj))
    else:
        opener = urllib.request.build_opener()
    opener.addheaders = [("User-Agent", UA)]
    return opener


def http_get(opener, url):
    req = urllib.request.Request(
        url, headers={"User-Agent": UA, "Referer": BASE_URL + "/"})
    return opener.open(req, timeout=45)


# ---------------------------------------------------------------------------
# Downloads
# ---------------------------------------------------------------------------

def fmt_ts(sec: float) -> str:
    ms = int(round(sec * 1000))
    h, rem = divmod(ms, 3_600_000)
    m, rem = divmod(rem, 60_000)
    s, ms = divmod(rem, 1000)
    return f"{h:02d}:{m:02d}:{s:02d}.{ms:03d}"


def transcript_to_vtt(paragraphs, duration):
    lines = ["WEBVTT", ""]
    for i, p in enumerate(paragraphs):
        start = float(p.get("timestamp", 0))
        if i + 1 < len(paragraphs):
            end = float(paragraphs[i + 1]["timestamp"])
        else:
            end = (duration if duration else start + 5.0)
        text = p.get("text", {}).get("__html", "")
        text = re.sub(r"<[^>]+>", "", html.unescape(text)).strip()
        lines.append(f"{fmt_ts(start)} --> {fmt_ts(end)}")
        lines.append(text)
        lines.append("")
    return "\n".join(lines)


def download_video(vid, dest_mp4):
    url = f"https://view.vzaar.com/{vid}/adaptive.m3u8"
    cmd = ["ffmpeg", "-y", "-loglevel", "error", "-i", url,
           "-c", "copy", "-bsf:a", "aac_adtstoasc", "-movflags", "+faststart", dest_mp4]
    r = subprocess.run(cmd, capture_output=True, text=True)
    ok = r.returncode == 0 and os.path.exists(dest_mp4) and os.path.getsize(dest_mp4) > 0
    if not ok:
        print(f"      ! ffmpeg failed: {r.stderr.strip()[:300]}", file=sys.stderr)
    return ok


def download_subtitle(opener, url, dest):
    try:
        resp = http_get(opener, url)
        data = resp.read()
        ct = resp.headers.get("Content-Type", "")
        if "text/html" in ct and (b"Just a moment" in data or b"challenge" in data.lower()):
            return False  # Cloudflare challenge
        with open(dest, "wb") as fh:
            fh.write(data)
        return True
    except Exception as e:
        print(f"      ! subtitle failed: {e}", file=sys.stderr)
        return False


def subtitle_basename(title, sub, index):
    lang = (sub.get("srcLang") or sub.get("label") or "sub").lower()
    lang = sanitize(lang).replace(" ", "_")
    if index:
        lang = f"{lang}.{index}"
    return f"{title}.{lang}.vtt"


# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------

def main():
    ap = argparse.ArgumentParser(
        description="Build a FutureLearn course tree and download videos/subtitles.")
    ap.add_argument("html_file", nargs="?", help="path to the saved course page")
    ap.add_argument("-o", "--out", default=".", help="output root (default: .)")
    ap.add_argument("--depth", type=int, choices=[1, 2, 3], default=3,
                    help="1=weeks, 2=weeks/activities, 3=weeks/activities/steps")
    ap.add_argument("--download", action="store_true",
                    help="fetch step pages and download videos + subtitles")
    ap.add_argument("--cookies", help="Netscape cookies.txt for futurelearn.com")
    ap.add_argument("--delay", type=float, default=1.0,
                    help="seconds between step-page fetches (default 1)")
    ap.add_argument("--limit", type=int, default=0,
                    help="only process the first N video steps (0 = all)")
    ap.add_argument("--skip-video", action="store_true")
    ap.add_argument("--skip-subs", action="store_true")
    ap.add_argument("--dry-run", action="store_true",
                    help="print tree + planned downloads, create/download nothing")
    args = ap.parse_args()

    src = args.html_file or (glob.glob("*.html") or [None])[0]
    if not src or not os.path.isfile(src):
        raise SystemExit("No HTML file given and none found in the current directory.")
    with open(src, "r", encoding="utf-8", errors="replace") as fh:
        html_text = fh.read()

    weeks = extract_weeks(html_text)
    root_name = sanitize(extract_run_title(html_text))
    root = os.path.join(args.out, root_name)
    items = collect_steps(weeks)

    # ---- phase 1: create the folder tree ---------------------------------
    created = 0
    for parts, _ in items:
        depth = min(args.depth, len(parts))
        path = os.path.join(root, *parts[:depth])
        if args.dry_run:
            continue
        os.makedirs(path, exist_ok=True)
        created += 1

    if args.depth > 1 or args.download:
        # (kept for symmetry; tree creation above already handled all depths)
        pass

    if not args.download:
        if args.dry_run:
            for parts, step in items:
                indent = "  " * (min(args.depth, len(parts)) - 1)
                print(indent + parts[min(args.depth, len(parts)) - 1])
            print(f"\n[dry-run] {len(items)} folders would be created under: {root}")
        else:
            print(f"Created {created} folders under: {root}")
        return

    # ---- phase 2: download videos + subtitles ----------------------------
    if args.dry_run:
        print(f"\nPlanned downloads ({sum(1 for _, s in items if s.get('type') == 'Video')} video steps):")
        for parts, step in items:
            if step.get("type") != "Video":
                continue
            print("  " + os.path.join(*parts))
        print("\n[dry-run] no network access made. Re-run without --dry-run to download.")
        return

    opener = make_opener(args.cookies)
    done = 0
    for parts, step in items:
        if args.limit and done >= args.limit:
            break
        if step.get("type") != "Video":
            continue

        folder = os.path.join(root, *parts)
        os.makedirs(folder, exist_ok=True)
        title = sanitize(step.get("title", ""))
        print(f"[{step.get('stepNumber')}] {title}")

        # fetch the step page to get the video id + subtitles
        url = BASE_URL + step.get("href", "")
        try:
            page = http_get(opener, url).read().decode("utf-8", "replace")
        except Exception as e:
            print(f"      ! could not fetch step page ({e}) — check your cookies", file=sys.stderr)
            continue

        info = extract_video_info(page)
        if not info:
            print("      (no video on this step)")
            continue

        vid = info["vzaarVideoId"]
        done += 1

        # video
        if not args.skip_video:
            dest = os.path.join(folder, f"{title}.mp4")
            if os.path.exists(dest) and os.path.getsize(dest) > 0:
                print(f"      video already present: {title}.mp4")
            else:
                print(f"      downloading video {vid} -> {title}.mp4")
                download_video(vid, dest)

        # subtitles
        if not args.skip_subs:
            seen = {}
            for sub in info["subtitles"]:
                src = sub.get("src")
                if not src:
                    continue
                lang = (sub.get("srcLang") or sub.get("label") or "sub").lower()
                seen[lang] = seen.get(lang, 0) + 1
                fname = subtitle_basename(title, sub, seen[lang] - 1)
                dest = os.path.join(folder, fname)
                if os.path.exists(dest) and os.path.getsize(dest) > 0:
                    print(f"      sub already present: {fname}")
                    continue
                print(f"      subtitle -> {fname}")
                if not download_subtitle(opener, src, dest):
                    print("      ! Cloudflare-blocked; trying embedded transcript...", file=sys.stderr)
                    vtt = transcript_to_vtt(info["transcript"], info["duration"])
                    if vtt.strip():
                        with open(os.path.join(folder, f"{title}.en.transcript.vtt"),
                                  "w", encoding="utf-8") as fh:
                            fh.write(vtt)
                        print("      wrote fallback transcript -> "
                              f"{title}.en.transcript.vtt")

        time.sleep(args.delay)

    print(f"\nDone. Processed {done} video step(s) under: {root}")


if __name__ == "__main__":
    main()
