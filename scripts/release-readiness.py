#!/usr/bin/env python3
"""Reject release builds with a missing consensus schedule or wrong tag."""

import argparse
import pathlib
import re


ROOT = pathlib.Path(__file__).resolve().parents[1]


def match(path: str, pattern: str, name: str) -> str:
    text = (ROOT / path).read_text(encoding="utf-8")
    found = re.search(pattern, text, re.MULTILINE)
    if not found:
        raise SystemExit(f"could not read {name} from {path}")
    return found.group(1)


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--tag", default="")
    args = parser.parse_args()

    version = match("scripts/package.py", r'^VERSION = "([^"]+)"$', "release version")
    height = int(match(
        "coreutils/chain/qday.go",
        r"^const QdayV1ActivationHeight uint64 = ([0-9]+)$",
        "protocol activation height",
    ))
    if height == 0:
        raise SystemExit("QDAY v1 protocol activation height is not scheduled")

    schedule_docs = (
        "README.md",
        "docs/consensus.md",
        "docs/integrations.md",
        "docs/parameters.md",
        "docs/protocol.md",
    )
    for path in schedule_docs:
        contents = (ROOT / path).read_text(encoding="utf-8")
        if "ACTIVATION_HEIGHT" in contents:
            raise SystemExit(f"{path} still contains the activation-height placeholder")
        if str(height) not in contents and f"{height:,}" not in contents:
            raise SystemExit(f"{path} does not document activation block {height}")

    if args.tag.startswith("v") and args.tag != f"v{version}":
        raise SystemExit(f"release tag {args.tag!r} does not match v{version}")
    print(f"QDAY v{version}: protocol activation at block {height}")


if __name__ == "__main__":
    main()
