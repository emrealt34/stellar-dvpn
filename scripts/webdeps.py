#!/usr/bin/env python3
"""Render the web app's direct dependencies as a Markdown table.

Reads `npm ls --depth=0 --json` for pinned versions and `npm outdated --json`
for the latest available version. Runs inside web/ by dependency-report.sh.
"""
import json
import subprocess


def main():
    # Only the direct dependencies declared in package.json, not the platform
    # specific optional packages npm hoists to depth 0.
    with open("package.json", encoding="utf-8") as fh:
        direct = json.load(fh).get("dependencies", {})
    current = json.loads(subprocess.check_output(["npm", "ls", "--depth=0", "--json"])).get(
        "dependencies", {}
    )
    try:
        outdated = json.loads(subprocess.check_output(["npm", "outdated", "--json"], stderr=subprocess.DEVNULL))
    except subprocess.CalledProcessError as exc:
        outdated = json.loads(exc.output or b"{}")
    print("| Package | Pinned | Latest |")
    print("| --- | --- | --- |")
    for name in direct:
        pinned = current.get(name, {}).get("version", "-")
        latest = outdated.get(name, {}).get("latest", "up to date")
        print("| {} | {} | {} |".format(name, pinned, latest))


if __name__ == "__main__":
    main()
