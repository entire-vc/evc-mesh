import unittest
from html.parser import HTMLParser

from linker_simhash_corpus_report import CASES, report


class ReportParser(HTMLParser):
    def __init__(self, document):
        super().__init__()
        self.elements = {}
        self.feed(document)

    def handle_starttag(self, tag, attrs):
        self.elements.setdefault(tag, []).append(dict(attrs))


def successful_log():
    return "\n".join([
        *("=== RUN   " + case for case in CASES),
        "    linker_simhash_threshold_test.go:72: unrelated pairs within 10 bits: 4 of 1225 (0.33%)",
        "    linker_simhash_threshold_test.go:102: worst 1-2 word edit: 8 bits",
        *("--- PASS: " + case + " (0.01s)" for case in CASES),
        "PASS",
    ])


class CorpusReportTest(unittest.TestCase):
    def test_preserves_observed_measurements_and_all_cases(self):
        suite = ReportParser(report(successful_log())).elements
        self.assertEqual(suite['testsuite'], [{
            'name': 'linker-simhash-corpus', 'tests': '3', 'failures': '0', 'errors': '0', 'skipped': '0',
        }])
        self.assertEqual([case['name'] for case in suite['testcase']], list(CASES))
        self.assertEqual({p['name']: p['value'] for p in suite['property']}, {
            "simhash.threshold_bits": "10", "simhash.unrelated_within": "4",
            "simhash.unrelated_total": "1225", "simhash.edit_max_bits": "8",
        })

    def test_refuses_incomplete_skipped_failed_or_inconsistent_runs(self):
        green = successful_log()
        for bad in (
            "testing: warning: no tests to run\nPASS\n",
            green.replace("--- PASS: " + CASES[1], "--- SKIP: " + CASES[1]),
            green.replace("--- PASS: " + CASES[2], "--- FAIL: " + CASES[2]),
            green.replace("=== RUN   " + CASES[0] + "\n", ""),
            green.replace("--- PASS: " + CASES[2] + " (0.01s)", ""),
            "\n".join(line for line in green.splitlines() if "worst 1-2" not in line),
            green + "\nworst 1-2 word edit: 8 bits",
            "\n".join(line for line in green.splitlines() if "unrelated pairs within" not in line),
            green + "\nunrelated pairs within 10 bits: 4 of 1225 (0.33%)",
            green.replace("(0.33%)", "(0.34%)"),
            green.replace("8 bits", "11 bits"),
            green.replace("4 of 1225", "40 of 1225"),
            green.replace("4 of 1225", "4 of 1224"),
            green.replace("(0.01s)", "(..s)"),
        ):
            with self.subTest(log=bad), self.assertRaises(ValueError):
                report(bad)


if __name__ == "__main__":
    unittest.main()
