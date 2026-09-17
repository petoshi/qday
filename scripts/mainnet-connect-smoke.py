#!/usr/bin/env python3
"""Verify a packaged native node against the live QDAY mainnet seeds."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import time
import urllib.request


ROOT = Path(__file__).resolve().parents[1]
HTTP = urllib.request.build_opener(urllib.request.ProxyHandler({}))
EXPECTED_GENESIS = "d71aebcb687c2fca4d3a5819e6c632efa7d46731395970fce081f3dc57606a40"
EXPECTED_WIRE_MAGIC = "514441590001a74e"
TARGET_SYSTEMS = {
    "linux-amd64": "linux",
    "linux-arm64": "linux",
    "windows-amd64": "windows",
    "macos-amd64": "macos",
    "macos-arm64": "macos",
}


def wait_for(check, label, seconds=180):
    deadline = time.monotonic() + seconds
    last = None
    while time.monotonic() < deadline:
        try:
            result = check()
            if result:
                return result
        except (OSError, ValueError) as error:
            last = error
        time.sleep(.25)
    raise RuntimeError("Timed out: " + label + (" (" + str(last) + ")" if last else ""))


def request(endpoint, token, route, body=None):
    req = urllib.request.Request(endpoint["url"] + "/api/" + route,
        data=None if body is None else json.dumps(body).encode(),
        headers={"Content-Type": "application/json", "Authorization": "Bearer " + token})
    with HTTP.open(req, timeout=15) as response:
        return json.load(response)


def package_directory(archive, target):
    suffix = ".zip" if TARGET_SYSTEMS[target] == "windows" else ".tar.gz"
    if not archive.name.endswith(suffix):
        raise RuntimeError("Unexpected package filename: " + archive.name)
    return archive.parent / archive.name.removesuffix(suffix)


def run_live(command, data, log_path, label):
    endpoint = None
    token = ""
    with log_path.open("w") as log:
        process = subprocess.Popen(command, stdout=log, stderr=log)
        try:
            def ready():
                if process.poll() is not None:
                    raise RuntimeError(label + " exited; see " + str(log_path.relative_to(ROOT)))
                endpoint_path = data / "node.json"
                token_path = data / "api.token"
                if not endpoint_path.exists() or not token_path.exists():
                    return None
                nonlocal endpoint, token
                endpoint = json.loads(endpoint_path.read_text())
                token = token_path.read_text().strip()
                status = request(endpoint, token, "status")
                if status["genesis"] != EXPECTED_GENESIS:
                    raise RuntimeError(label + " loaded the wrong genesis")
                if status["p2p"]["wireMagic"] != EXPECTED_WIRE_MAGIC:
                    raise RuntimeError(label + " loaded the wrong wire magic")
                if status["network"] != "qday-mainnet" or status["development"]:
                    raise RuntimeError(label + " is not running QDAY mainnet")
                if status["hasWallet"] or status["mode"] != "STOP":
                    raise RuntimeError(label + " unexpectedly loaded wallet keys")
                return status if status["peers"] > 0 and status["networkSynced"] and status["synced"] else None

            status = wait_for(ready, label + " live seed handshake and synchronization")
            request(endpoint, token, "shutdown", {})
            if process.wait(timeout=15) != 0:
                raise RuntimeError(label + " returned a failure status during shutdown")
        finally:
            if process.poll() is None and endpoint and token:
                try:
                    request(endpoint, token, "shutdown", {})
                    process.wait(timeout=15)
                except (OSError, ValueError, RuntimeError, subprocess.TimeoutExpired):
                    pass
            if process.poll() is None:
                if os.name == "nt":
                    # The desktop executable owns a child node process. Killing
                    # only the launcher leaves SQLite open on Windows, so stop
                    # the complete process tree when graceful shutdown fails.
                    subprocess.run(
                        ["taskkill", "/PID", str(process.pid), "/T", "/F"],
                        stdout=subprocess.DEVNULL,
                        stderr=subprocess.DEVNULL,
                        timeout=15,
                        check=False,
                    )
                else:
                    process.terminate()
                try:
                    process.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait()
    node_log = data / "node.log"
    if token and (token in log_path.read_text() or (node_log.exists() and token in node_log.read_text())):
        raise RuntimeError("Local API token was written to the " + label + " log")
    return status


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--target", required=True, choices=TARGET_SYSTEMS)
    parser.add_argument("--report", type=Path, default=ROOT / "build/distribution-report.json")
    args = parser.parse_args()
    packages = json.loads(args.report.read_text())["packages"]
    package = next((item for item in packages if item["target"] == args.target), None)
    if package is None:
        raise RuntimeError("Package target is missing from the distribution report")
    if package["genesis"] != EXPECTED_GENESIS:
        raise RuntimeError("Distribution report contains the wrong mainnet genesis")
    distribution = package_directory(ROOT / package["archive"], package["target"])
    node_distribution = package_directory(ROOT / package["nodeArchive"], package["target"])
    system = TARGET_SYSTEMS[args.target]
    executable = distribution / ("QDAY-Wallet.exe" if system == "windows" else "QDAY-Wallet")
    manifest = distribution / "resources/qday-mainnet.json"
    node_executable = node_distribution / ("qday.exe" if system == "windows" else "qday")
    node_manifest = node_distribution / "qday-mainnet.json"
    if hashlib.sha256(manifest.read_bytes()).hexdigest() != package["manifestSHA256"]:
        raise RuntimeError("Embedded mainnet manifest hash mismatch")
    if node_manifest.read_bytes() != manifest.read_bytes():
        raise RuntimeError("Standalone node and wallet embed different mainnet manifests")
    host_system = "windows" if os.name == "nt" else "macos" if sys.platform == "darwin" else "linux"
    if system != host_system:
        raise RuntimeError("Run this check on the package's native operating system")

    seeds = json.loads((distribution / "resources/distribution.json").read_text())["seeds"]
    if seeds != ["seed1.pqday.com:19771", "seed2.pqday.com:19771", "seed3.pqday.com:19771"]:
        raise RuntimeError("Packaged bootstrap seed list mismatch")

    with tempfile.TemporaryDirectory(prefix="qday live mainnet ") as temp:
        temp = Path(temp)
        wallet_data = temp / "wallet node data"
        status = run_live(
            [str(executable), "--data", str(wallet_data), "--no-browser", "--upnp=false"],
            wallet_data,
            ROOT / "build" / ("live-mainnet-wallet-" + args.target + ".log"),
            "Packaged wallet",
        )
        daemon_data = temp / "standalone node data"
        daemon_status = run_live(
            [str(node_executable), "--network", str(node_manifest), "--data", str(daemon_data),
             "--http", "127.0.0.1:0", "--p2p", "auto", "--upnp=false"],
            daemon_data,
            ROOT / "build" / ("live-mainnet-node-" + args.target + ".log"),
            "Standalone node",
        )
    result = {
        "result": "PASS",
        "target": args.target,
        "genesis": status["genesis"],
        "height": status["height"],
        "peers": status["peers"],
        "nodeHeight": daemon_status["height"],
        "nodePeers": daemon_status["peers"],
        "wireMagic": status["p2p"]["wireMagic"],
        "seeds": seeds,
    }
    print(json.dumps(result, indent=2))


if __name__ == "__main__":
    main()
