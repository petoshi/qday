#!/usr/bin/env python3
"""Verify a QDAY release archive without executing it."""
import argparse
import hashlib
import json
from pathlib import Path, PurePosixPath
import struct
import tarfile
import zipfile


ROOT = Path(__file__).resolve().parents[1]
EXPECTED_GENESIS = "d71aebcb687c2fca4d3a5819e6c632efa7d46731395970fce081f3dc57606a40"
EXPECTED_MANIFEST_SHA256 = "14d4a47a850f5ba9d81129142d8718c323d826b5f9459516374c4f0f92eaa65b"
EXPECTED_SEEDS = ["seed1.pqday.com:19771", "seed2.pqday.com:19771", "seed3.pqday.com:19771"]
TARGETS = {"linux-amd64", "linux-arm64", "windows-amd64", "macos-amd64", "macos-arm64"}


def selected_package(report, target):
    package = next((item for item in report["packages"] if item["target"] == target), None)
    if package is None:
        raise RuntimeError("Package target is missing from the distribution report")
    return package


def safe_names(names, folder):
    for name in names:
        path = PurePosixPath(name)
        if "\\" in name or path.is_absolute() or ".." in path.parts or not path.parts or path.parts[0] != folder:
            raise RuntimeError("Unsafe archive path: " + name)


def verify_executable(data, target, name):
    if len(data) < 1_000_000:
        raise RuntimeError("Native executable is unexpectedly small: " + name)
    if target.startswith("linux"):
        machine = struct.unpack_from("<H", data, 18)[0] if data[:4] == b"\x7fELF" else None
        expected = 62 if target.endswith("amd64") else 183
    elif target.startswith("windows"):
        offset = struct.unpack_from("<I", data, 60)[0] if data[:2] == b"MZ" else 0
        machine = struct.unpack_from("<H", data, offset + 4)[0] if data[offset:offset + 4] == b"PE\0\0" else None
        expected = 0x8664
    else:
        machine = struct.unpack_from("<I", data, 4)[0] if data[:4] == b"\xcf\xfa\xed\xfe" else None
        expected = 0x01000007 if target.endswith("amd64") else 0x0100000c
    if machine != expected:
        raise RuntimeError("Native executable architecture mismatch: " + name)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--target", required=True, choices=sorted(TARGETS))
    parser.add_argument("--report", type=Path, default=ROOT / "build/distribution-report.json")
    args = parser.parse_args()
    package = selected_package(json.loads(args.report.read_text()), args.target)
    archive = ROOT / package["archive"]
    suffix = ".zip" if args.target.startswith("windows") else ".tar.gz"
    if not archive.name.endswith(suffix):
        raise RuntimeError("Unexpected archive filename")
    folder = archive.name.removesuffix(suffix)
    digest = hashlib.sha256(archive.read_bytes()).hexdigest()
    checksum_path = Path(str(archive) + ".sha256")
    checksum = checksum_path.read_text().strip().split()
    if checksum != [digest, archive.name] or package["sha256"] != digest:
        raise RuntimeError("Archive checksum mismatch")

    if args.target.startswith("windows"):
        bundle = zipfile.ZipFile(archive)
        names = bundle.namelist()
        safe_names(names, folder)
        read = bundle.read
    else:
        bundle = tarfile.open(archive)
        members = bundle.getmembers()
        if any(not (member.isfile() or member.isdir()) for member in members):
            raise RuntimeError("Release tar contains links or special files")
        names = [member.name for member in members]
        safe_names(names, folder)

        def read(name):
            stream = bundle.extractfile(name)
            if stream is None:
                raise RuntimeError("Archive file is missing: " + name)
            return stream.read()

    try:
        manifest = read(folder + "/resources/qday-mainnet.json")
        config = json.loads(read(folder + "/resources/distribution.json"))
        instructions = read(folder + "/START-HERE.txt").decode("utf-8")
        suffix_exe = ".exe" if args.target.startswith("windows") else ""
        for executable in (folder + "/QDAY-Wallet" + suffix_exe, folder + "/resources/qday-node" + suffix_exe):
            if executable not in names:
                raise RuntimeError("Native executable is missing: " + executable)
            verify_executable(read(executable), args.target, executable)
    finally:
        bundle.close()

    forbidden = {"wallet.key", "api.token", "node.json", "p2p.json"}
    if any(PurePosixPath(name).name in forbidden or name.endswith((".db", ".sqlite", ".sqlite3", ".log")) for name in names):
        raise RuntimeError("Release archive contains private or runtime state")
    manifest_sha = hashlib.sha256(manifest).hexdigest()
    if manifest_sha != EXPECTED_MANIFEST_SHA256 or manifest != (ROOT / "qday-mainnet.json").read_bytes():
        raise RuntimeError("Embedded mainnet manifest bytes do not match the published manifest")
    if package["genesis"] != EXPECTED_GENESIS or package["manifestSHA256"] != EXPECTED_MANIFEST_SHA256:
        raise RuntimeError("Distribution report contains the wrong mainnet identity")
    if config != {"format": 1, "network": "mainnet", "seeds": EXPECTED_SEEDS}:
        raise RuntimeError("Embedded distribution configuration mismatch")
    if "No Go, Python, Node.js, Docker" not in instructions:
        raise RuntimeError("Start instructions are missing from the release")
    print(json.dumps({
        "result": "PASS",
        "target": args.target,
        "archive": archive.name,
        "sha256": digest,
        "genesis": EXPECTED_GENESIS,
        "manifestSHA256": manifest_sha,
        "files": len(names),
    }, indent=2))


if __name__ == "__main__":
    main()
