from __future__ import annotations

import hashlib
import io
import json
import os
import tarfile
from typing import Any, Dict, Optional

from . import constants as C
from .contracts import ContractError

MANIFEST_NAME = "manifest.json"
STATE_NAME = "agent/state.bin"
BASELINE_NAME = "baseline.json"
STEERING_NAME = "steering.json"

DEFAULT_MAX_PACKAGE_BYTES = 512 * 1024 * 1024


class PackageError(Exception):
    pass


def _safe_member(name: str) -> str:
    n = name.replace("\\", "/")
    if n.startswith("/") or n.startswith("../"):
        raise PackageError(f"unsafe path in package: {name!r}")
    for part in n.split("/"):
        if part == "..":
            raise PackageError(f"unsafe path in package: {name!r}")
    return n


def _file_meta_bytes(data: bytes) -> Dict[str, Any]:
    return {"sha256": hashlib.sha256(data).hexdigest(), "size_bytes": len(data)}


def _file_meta(path: str) -> Dict[str, Any]:
    h = hashlib.sha256()
    size = 0
    with open(path, "rb") as fh:
        for chunk in iter(lambda: fh.read(1024 * 1024), b""):
            h.update(chunk)
            size += len(chunk)
    return {"sha256": h.hexdigest(), "size_bytes": size}


def _walk_files(subdir: str, prefix: str) -> Dict[str, Dict[str, Any]]:
    entries: Dict[str, Dict[str, Any]] = {}
    if not os.path.isdir(subdir):
        return entries
    for root, _dirs, files in sorted(os.walk(subdir)):
        for fn in sorted(files):
            full = os.path.join(root, fn)
            rel = os.path.relpath(full, subdir).replace(os.sep, "/")
            name = f"{prefix}/{rel}"
            _safe_member(name)
            entries[name] = _file_meta(full)
    return entries


def build_package(
    workspace_dir: str,
    output_dir: str,
    state_bytes: bytes,
    baseline_json: Any,
    steering_json: Any,
    dest_tgz: str,
    contract_version: str,
    run_id: str,
    stage: int,
    fence: int,
    state_format: str,
    image_digest: str = "",
    max_bytes: int = DEFAULT_MAX_PACKAGE_BYTES,
) -> Dict[str, Any]:
    entries: Dict[str, Dict[str, Any]] = {STATE_NAME: _file_meta_bytes(state_bytes)}
    entries[BASELINE_NAME] = _file_meta_bytes(json.dumps(baseline_json, ensure_ascii=False).encode())
    entries[STEERING_NAME] = _file_meta_bytes(json.dumps(steering_json, ensure_ascii=False).encode())
    for subdir, prefix in ((workspace_dir, "workspace"), (output_dir, "output")):
        entries.update(_walk_files(subdir, prefix))

    total_size = sum(e["size_bytes"] for e in entries.values())
    manifest = {
        "contract_version": contract_version,
        "image_digest": image_digest,
        "state_format": state_format,
        "run_id": run_id,
        "stage": stage,
        "fence": fence,
        "entries": entries,
        "total_size_bytes": total_size,
    }
    manifest_bytes = json.dumps(manifest, ensure_ascii=False).encode()
    total_bytes = total_size + len(manifest_bytes)
    if total_bytes > max_bytes:
        raise PackageError("package exceeds allowed size")

    os.makedirs(os.path.dirname(dest_tgz) or ".", exist_ok=True)
    with tarfile.open(dest_tgz, "w:gz") as tar:
        _add_bytes(tar, MANIFEST_NAME, manifest_bytes)
        _add_bytes(tar, STATE_NAME, state_bytes)
        _add_bytes(tar, BASELINE_NAME, json.dumps(baseline_json, ensure_ascii=False).encode())
        _add_bytes(tar, STEERING_NAME, json.dumps(steering_json, ensure_ascii=False).encode())
        for subdir, prefix in ((workspace_dir, "workspace"), (output_dir, "output")):
            if not os.path.isdir(subdir):
                continue
            for root, dirs, files in sorted(os.walk(subdir)):
                for d in sorted(dirs):
                    named = os.path.join(root, d)
                    rel = os.path.relpath(named, subdir).replace(os.sep, "/")
                    name = f"{prefix}/{rel}" if rel != "." else prefix
                    _safe_member(name)
                    tar.add(named, arcname=name, recursive=False)
                for fn in sorted(files):
                    full = os.path.join(root, fn)
                    rel = os.path.relpath(full, subdir).replace(os.sep, "/")
                    name = f"{prefix}/{rel}"
                    _safe_member(name)
                    tar.add(full, arcname=name, recursive=False)
    return manifest


def _add_bytes(tar: tarfile.TarFile, name: str, data: bytes) -> None:
    _safe_member(name)
    info = tarfile.TarInfo(name)
    info.size = len(data)
    tar.addfile(info, io.BytesIO(data))


def validate_archive(tgz_path: str, max_bytes: int = DEFAULT_MAX_PACKAGE_BYTES) -> None:
    size = os.path.getsize(tgz_path)
    if size > max_bytes:
        raise PackageError("package exceeds allowed size")
    seen = set()
    with tarfile.open(tgz_path, "r:gz") as tar:
        for m in tar.getmembers():
            name = _safe_member(m.name)
            if name in seen:
                raise PackageError(f"duplicate entry {name!r}")
            seen.add(name)
            if m.islnk() or m.issym():
                raise PackageError(f"link not allowed in package: {name!r}")
            if not (m.isfile() or m.isdir()):
                raise PackageError(f"special file not allowed in package: {name!r}")


def restore_package(
    tgz_path: str,
    dest_workspace: str,
    dest_output: str,
    dest_app: str,
    max_bytes: int = DEFAULT_MAX_PACKAGE_BYTES,
) -> "RestoredPackage":
    validate_archive(tgz_path, max_bytes)
    manifest: Optional[Dict[str, Any]] = None
    state_bytes: Optional[bytes] = None
    baseline: Any = None
    steering: Any = None

    with tarfile.open(tgz_path, "r:gz") as tar:
        members = tar.getmembers()
        for m in members:
            if m.name == MANIFEST_NAME:
                f = tar.extractfile(m)
                manifest = json.loads(f.read().decode("utf-8")) if f else None
            elif m.name == STATE_NAME:
                f = tar.extractfile(m)
                if f is None:
                    raise PackageError(f"cannot read {STATE_NAME!r} from package")
                state_bytes = f.read()
            elif m.name == STEERING_NAME:
                f = tar.extractfile(m)
                steering = json.loads(f.read().decode("utf-8")) if f else None
            elif m.name == BASELINE_NAME:
                f = tar.extractfile(m)
                baseline = json.loads(f.read().decode("utf-8")) if f else None

    if manifest is None or state_bytes is None:
        raise PackageError("package missing manifest.json or agent/state.bin")
    if not isinstance(manifest, dict) or not isinstance(manifest.get("entries", {}), dict):
        raise PackageError("package manifest missing entries listing")
    entries = manifest["entries"]

    _wipe(dest_workspace)
    _wipe(dest_output)
    total_unpacked = 0

    with tarfile.open(tgz_path, "r:gz") as tar:
        for m in tar.getmembers():
            name = _safe_member(m.name)
            if not (name.startswith("workspace/") or name.startswith("output/")):
                continue
            if m.isdir():
                prefix, rest = name.split("/", 1)
                target = dest_workspace if prefix == "workspace" else dest_output
                try:
                    os.makedirs(os.path.join(target, rest), exist_ok=True)
                except OSError as exc:
                    raise PackageError(f"cannot create directory {name!r}: {exc}")
                continue
            if not m.isfile():
                raise PackageError(f"only regular files/dirs may be restored: {name!r}")
            declared = entries.get(name)
            if not isinstance(declared, dict):
                raise PackageError(f"member {name!r} is not declared in package manifest")
            prefix, rest = name.split("/", 1)
            target = dest_workspace if prefix == "workspace" else dest_output
            dest = os.path.join(target, rest)
            os.makedirs(os.path.dirname(dest), exist_ok=True)

            h = hashlib.sha256()
            size = 0
            f = tar.extractfile(m)
            with open(dest, "wb") as out:
                if f:
                    chunk = f.read(1024 * 1024)
                    while chunk:
                        out.write(chunk)
                        h.update(chunk)
                        size += len(chunk)
                        total_unpacked += len(chunk)
                        if total_unpacked > max_bytes:
                            raise PackageError("package exceeds allowed size")
                        chunk = f.read(1024 * 1024)
            expect_size = declared.get("size_bytes")
            expect_sha = declared.get("sha256")
            if size != expect_size or h.hexdigest() != expect_sha:
                try:
                    os.remove(dest)
                except OSError:
                    pass
                raise PackageError(f"member {name!r} failed hash/size verification")

    return RestoredPackage(
        state_bytes=state_bytes,
        manifest=manifest,
        baseline=baseline,
        steering=steering,
    )


def _wipe(path: str) -> None:
    if os.path.exists(path):
        for root, _dirs, files in os.walk(path, topdown=False):
            for fn in files:
                os.unlink(os.path.join(root, fn))
            for d in _dirs:
                os.rmdir(os.path.join(root, d))
    os.makedirs(path, exist_ok=True)


class RestoredPackage:

    def __init__(self, state_bytes: bytes, manifest: Dict[str, Any],
                 baseline: Any, steering: Any):
        self.state_bytes = state_bytes
        self.manifest = manifest
        self.baseline = baseline
        self.steering = steering

    @property
    def state_format(self) -> str:
        return self.manifest.get("state_format", "")

    @property
    def image_digest(self) -> str:
        return self.manifest.get("image_digest", "")

    @property
    def steering_cursor(self) -> int:
        if isinstance(self.steering, dict):
            return int(self.steering.get("incorporated_through_seq", 0))
        return 0


__all__ = [
    "PackageError", "build_package", "restore_package", "validate_archive",
    "RestoredPackage", "MANIFEST_NAME", "STATE_NAME", "BASELINE_NAME", "STEERING_NAME",
]
