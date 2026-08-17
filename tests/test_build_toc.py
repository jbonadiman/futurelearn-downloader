import os
import sys
import unittest

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "src"))

from futurelearn_downloader.scrape import build_toc, course_source_url


def _item(week, act, stepdir, title, href=None):
    step = {"title": title}
    if href:
        step["href"] = href
    return ([week, act, stepdir], step)


class BuildTocFrontmatterTests(unittest.TestCase):
    def test_source_url_is_written_as_yaml_frontmatter(self):
        items = [
            _item("Week 1 - X", "1. Intro", "1.1 Welcome", "Welcome",
                  href="/courses/foo/5/steps/101"),
        ]
        toc = build_toc("Course", items,
                        source_url="https://www.futurelearn.com/courses/foo/5/steps/101")
        self.assertTrue(toc.startswith(
            "---\nsource: https://www.futurelearn.com/courses/foo/5/steps/101\n---\n"))
        self.assertIn("# Course\n", toc)

    def test_no_frontmatter_when_no_source(self):
        items = [_item("Week 1 - X", "1. Intro", "1.1 Welcome", "Welcome")]
        toc = build_toc("Course", items)
        self.assertTrue(toc.startswith("# Course\n"))
        self.assertNotIn("---", toc.split("\n")[0])


class CourseSourceUrlTests(unittest.TestCase):
    def test_first_step_href_becomes_absolute_source(self):
        items = [_item("Week 1 - X", "1. Intro", "1.1 Welcome", "Welcome",
                       href="/courses/foo/5/steps/101")]
        self.assertEqual(course_source_url(items),
                         "https://www.futurelearn.com/courses/foo/5/steps/101")

    def test_empty_when_no_href(self):
        self.assertEqual(course_source_url([]), "")
        self.assertEqual(course_source_url([(["a", "b", "c"], {"title": "x"})]), "")


if __name__ == "__main__":
    unittest.main()
