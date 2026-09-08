#!/usr/bin/env python3
"""Push README.md to the Docker Hub repository overview.

Reads DOCKERHUB_USERNAME and DOCKERHUB_PASSWORD (an access token works) from
the environment. Run from the repository root.
"""
import json
import os
import subprocess
import sys
import urllib.error
import urllib.request

REPOSITORY = "pavanputhra/logspout-signoz"
MAX_DESCRIPTION = 25000


def post(url: str, payload: dict, headers: dict, method: str = "POST") -> dict:
    request = urllib.request.Request(
        url, data=json.dumps(payload).encode(),
        headers={"Content-Type": "application/json", **headers}, method=method)
    try:
        return json.loads(urllib.request.urlopen(request).read() or b"{}")
    except urllib.error.HTTPError as error:
        detail = error.read()[:300].decode(errors="replace")
        sys.exit(f"{method} {url} failed: {error.code} {detail}")


def main() -> None:
    username = os.environ.get("DOCKERHUB_USERNAME")
    password = os.environ.get("DOCKERHUB_PASSWORD")
    if not username or not password:
        sys.exit("DOCKERHUB_USERNAME and DOCKERHUB_PASSWORD must be set")

    description = subprocess.run(
        [sys.executable, "scripts/dockerhub-description.py"],
        capture_output=True, text=True, check=True).stdout
    if len(description) > MAX_DESCRIPTION:
        sys.exit(f"description is {len(description)} bytes, over Docker Hub's {MAX_DESCRIPTION} limit")

    token = post("https://hub.docker.com/v2/users/login/",
                 {"username": username, "password": password}, {})["token"]

    post(f"https://hub.docker.com/v2/repositories/{REPOSITORY}/",
         {"full_description": description},
         {"Authorization": f"JWT {token}"}, method="PATCH")

    print(f"Docker Hub overview updated ({len(description)} bytes)")


if __name__ == "__main__":
    main()
