"""Report distinct unit/full coverage; optionally enforce the 100% prerequisite.

Passing this check alone NEVER proves module equivalence or authorizes deletion.
Inputs must come from scripts/check-parity.sh on the same checkout.
"""

import argparse
import json
from pathlib import Path


def go_counts(path: Path) -> tuple[int, int]:
    lines = path.read_text().splitlines()
    if not lines or lines[0] not in {"mode: set", "mode: count", "mode: atomic"}:
        raise ValueError(f"invalid Go coverage profile: {path}")
    blocks = {}
    for line in lines[1:]:
        location, statements, hits = line.split()
        count, hits = int(statements), int(hits)
        if count < 0 or hits < 0:
            raise ValueError("negative coverage counts")
        old = blocks.get(location)
        if old is not None and old[0] != count:
            raise ValueError(f"inconsistent coverage block: {location}")
        blocks[location] = (count, hits > 0 or (old is not None and old[1]))
    total = sum(count for count, _ in blocks.values())
    covered = sum(count for count, hit in blocks.values() if hit)
    if total == 0:
        raise ValueError("empty Go coverage denominator")
    return covered, total


def python_counts(path: Path) -> tuple[int, int, int, int, int]:
    report = json.loads(path.read_text())
    if not report["meta"]["branch_coverage"]:
        raise ValueError("Python branch coverage is required")
    totals = report["totals"]
    counts = tuple(
        totals[key]
        for key in [
            "covered_lines",
            "num_statements",
            "covered_branches",
            "num_branches",
            "excluded_lines",
        ]
    )
    covered, total, branches, branch_total, excluded = counts
    if (
        not all(isinstance(x, int) and x >= 0 for x in counts)
        or not 0 <= covered <= total
        or not 0 <= branches <= branch_total
        or total == 0
    ):
        raise ValueError("invalid Python coverage counts")
    return covered, total, branches, branch_total, excluded


def complete(go: tuple[int, int], python: tuple[int, int, int, int, int]) -> bool:
    # Compare integers, never rounded displays such as 99.999% -> "100%".
    return (
        go[1] > 0
        and python[1] > 0
        and go[0] == go[1]
        and python[0] == python[1]
        and python[2] == python[3]
        and python[4] == 0
    )


def percent(covered: int, total: int) -> str:
    return f"{covered}/{total} ({100 * covered / total:.2f}%)" if total else "0/0 (n/a)"


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("directory", type=Path)
    parser.add_argument("--require-complete-unit", action="store_true")
    args = parser.parse_args()
    results = {}
    for suite in ["unit", "all"]:
        go = go_counts(args.directory / f"go-{suite}.out")
        python = python_counts(args.directory / f"python-{suite}.json")
        results[suite] = (go, python)
        print(f"{suite}: Go statements {percent(*go)}")
        print(
            f"{suite}: Python statements {percent(*python[:2])}; "
            f"branches {percent(*python[2:4])}; excluded lines {python[4]}"
        )
    if (
        results["unit"][0][1] != results["all"][0][1]
        or results["unit"][1][1] != results["all"][1][1]
        or results["unit"][1][3] != results["all"][1][3]
    ):
        raise ValueError("unit/full coverage denominators differ; rerun the complete script")
    print("Coverage alone does NOT prove equivalence. See docs/python-retirement.md.")
    if args.require_complete_unit and not complete(*results["unit"]):
        print("BLOCKED: the exact 100% unit-coverage prerequisite is not met.")
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
