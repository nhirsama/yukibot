"""The retirement prerequisite must not pass on rounded or excluded coverage."""

import json

import pytest

from scripts.coverage_summary import complete, go_counts, python_counts


def test_exact_coverage_gate():
    assert not complete((0, 0), (0, 0, 0, 0, 0))
    assert complete((10, 10), (10, 10, 4, 4, 0))
    assert not complete((999999, 1000000), (10, 10, 4, 4, 0))
    assert not complete((10, 10), (9, 10, 4, 4, 0))
    assert not complete((10, 10), (10, 10, 3, 4, 0))
    assert not complete((10, 10), (10, 10, 4, 4, 1))


def test_go_counts_union_repeated_blocks(tmp_path):
    path = tmp_path / "go.out"
    path.write_text("mode: atomic\na.go:1.1,2.1 3 0\na.go:1.1,2.1 3 1\na.go:3.1,4.1 2 0\n")
    assert go_counts(path) == (3, 5)


@pytest.mark.parametrize(
    "text",
    [
        "",
        "mode: atomic\n",
        "mode: invalid\n",
        "mode: atomic\na:1 2 -1\n",
        "mode: atomic\na:1 2 1\na:1 3 1\n",
    ],
)
def test_invalid_go_profile_fails_closed(tmp_path, text):
    path = tmp_path / "go.out"
    path.write_text(text)
    with pytest.raises(ValueError):
        go_counts(path)


def test_python_requires_branch_data(tmp_path):
    path = tmp_path / "python.json"
    path.write_text(json.dumps({"meta": {"branch_coverage": False}}))
    with pytest.raises(ValueError, match="branch"):
        python_counts(path)


@pytest.mark.parametrize("covered, total", [(11, 10), (-1, 10), (0, 0)])
def test_invalid_python_counts_fail_closed(tmp_path, covered, total):
    path = tmp_path / "python.json"
    path.write_text(
        json.dumps(
            {
                "meta": {"branch_coverage": True},
                "totals": {
                    "covered_lines": covered,
                    "num_statements": total,
                    "covered_branches": 1,
                    "num_branches": 1,
                    "excluded_lines": 0,
                },
            }
        )
    )
    with pytest.raises(ValueError):
        python_counts(path)
