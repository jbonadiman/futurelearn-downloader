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
Shared helpers for futurelearn-downloader: name sanitising and course-tree parsing.

The course outline (weeks -> activities -> steps) lives in an embedded JSON blob inside
a course page. These functions turn that blob into a flat list of steps and safe,
collision-free folder/file names.
"""

import html
import json
import re
import unicodedata

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
            raise ValueError("Could not find a course structure in this HTML.")
    return json.loads(m.group(1))["courseContent"]["weeks"]


def extract_run_title(html_text: str) -> str:
    m = re.search(r'"runTitle":"(.*?)"', html_text)
    return m.group(1) if m else "course"


def week_folder_name(week):
    wnum = week.get("number", "")
    wtitle = sanitize(week.get("title", ""))
    return f"Week {wnum} - {wtitle}" if wtitle else f"Week {wnum}"


def locked_weeks(weeks):
    """Weeks not yet released, as [(folder_name, number, unlocks_at)].

    The week-level `locked` flag is the only reliable signal. On a drip-fed course
    every step still reports `isLocked: false`, even inside a locked week, so a
    per-step check finds nothing and the whole course looks available.
    """
    return [(week_folder_name(w), w.get("number", ""), w.get("weekUnlocksAt") or "")
            for w in weeks if w.get("locked")]


def collect_steps(weeks, include_locked=True):
    """Flatten weeks -> activities -> steps into (folder_parts, step) tuples.

    A locked week's steps still serve their real content to an enrolled session — the
    lock is a UI gate on the drip-feed schedule, not a server-side restriction — so they
    are included by default and the caller warns about them (see `locked_weeks`).
    Pass `include_locked=False` to take only what has been released.
    """
    items = []
    for week in weeks:
        if week.get("locked") and not include_locked:
            continue
        week_name = week_folder_name(week)
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
# Naming
# ---------------------------------------------------------------------------

def subtitle_basename(title, sub, index):
    lang = (sub.get("srcLang") or sub.get("label") or "sub").lower()
    lang = sanitize(lang).replace(" ", "_")
    if index:
        lang = f"{lang}.{index}"
    return f"{title}.{lang}.vtt"
