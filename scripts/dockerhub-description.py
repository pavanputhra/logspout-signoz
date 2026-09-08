#!/usr/bin/env python3
"""Print README.md adapted for the Docker Hub overview.

Docker Hub renders the description outside the repository, so relative links
resolve against hub.docker.com and 404. They are rewritten to absolute GitHub
URLs; anchors and absolute URLs are left alone.
"""
import re
import sys

REPO = "https://github.com/pavanputhra/logspout-signoz/blob/main/"

def absolutise(markdown: str) -> str:
    def replace(match: "re.Match[str]") -> str:
        text, target = match.group(1), match.group(2)
        if target.startswith(("http://", "https://", "#", "mailto:")):
            return match.group(0)
        # Strip only a leading "./" — lstrip("./") would eat the dot in
        # ".github/workflows/...".
        if target.startswith("./"):
            target = target[2:]
        return f"[{text}]({REPO}{target})"

    return re.sub(r"\[([^\]]*)\]\(([^)\s]+)\)", replace, markdown)

if __name__ == "__main__":
    path = sys.argv[1] if len(sys.argv) > 1 else "README.md"
    with open(path, encoding="utf-8") as handle:
        sys.stdout.write(absolutise(handle.read()))
