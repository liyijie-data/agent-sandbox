from __future__ import annotations

import glob
import hashlib
import io
import os
import shutil
import tarfile
import tempfile
import urllib.error
import urllib.parse
import urllib.request
import zipfile
from dataclasses import dataclass
from typing import Any, Dict, List, Optional, Tuple

ERR_DOWNLOAD_FAILED = "resource_download_failed"
ERR_DOWNLOAD_TOO_LARGE = "resource_download_too_large"
ERR_SIZE_MISMATCH = "resource_size_mismatch"
ERR_HASH_MISMATCH = "resource_hash_mismatch"
ERR_UNSAFE_PATH = "resource_unsafe_path"
ERR_DUPLICATE_PATH = "resource_duplicate_path"
ERR_INVALID_ENTRY = "resource_invalid_entry"
ERR_SKILL_UNSUPPORTED_FORMAT = "skill_unsupported_format"
ERR_SKILL_ARCHIVE_UNSAFE = "skill_archive_unsafe"
ERR_SKILL_LIMIT_EXCEEDED = "skill_limit_exceeded"
ERR_SKILL_MISSING_MANIFEST = "skill_missing_manifest"
ERR_SKILL_REBUILD_FAILED = "skill_rebuild_failed"

_CHUNK = 64 * 1024


class ResourceError(Exception):

    def __init__(self, code: str, reason: str = ""):
        super().__init__(f"{code}: {reason}")
        self.code = code
        self.reason = reason


@dataclass
class ResourceLimits:

    max_download_bytes: int = 256 * 1024 * 1024
    download_timeout: float = 30.0
    max_archive_bytes: int = 64 * 1024 * 1024
    max_unpacked_bytes: int = 512 * 1024 * 1024
    max_archive_entries: int = 4096
    max_member_bytes: int = 64 * 1024 * 1024
    max_skill_md_bytes: int = 1024 * 1024




def validate_safe_relpath(name: str, context: str = "path") -> str:
    if not isinstance(name, str) or not name:
        raise ResourceError(ERR_UNSAFE_PATH, f"{context}: empty")
    if name.startswith("/") or "\\" in name:
        raise ResourceError(ERR_UNSAFE_PATH, f"{context}: absolute or backslash: {name!r}")
    if any(ord(c) < 0x20 for c in name):
        raise ResourceError(ERR_UNSAFE_PATH, f"{context}: control characters")
    parts = name.split("/")
    if any(p in ("", ".", "..") for p in parts):
        raise ResourceError(ERR_UNSAFE_PATH, f"{context}: traversal or empty segment: {name!r}")
    return name


def validate_skill_name(name: str) -> str:
    if not isinstance(name, str) or not name:
        raise ResourceError(ERR_UNSAFE_PATH, "skill name: empty")
    if "/" in name or "\\" in name or name in (".", "..") or len(name) > 128:
        raise ResourceError(ERR_UNSAFE_PATH, f"skill name: unsafe {name!r}")
    if any(ord(c) < 0x20 for c in name):
        raise ResourceError(ERR_UNSAFE_PATH, "skill name: control characters")
    return name


def _validate_download_url(url: str) -> None:
    try:
        parts = urllib.parse.urlsplit(url)
    except ValueError:
        raise ResourceError(ERR_INVALID_ENTRY, f"invalid download url {url!r}")
    if parts.scheme not in ("http", "https") or not parts.netloc:
        raise ResourceError(ERR_INVALID_ENTRY, f"unsupported download url {url!r}")
    if parts.username or parts.password:
        raise ResourceError(ERR_INVALID_ENTRY, "credentials in download url are not allowed")




def _download_to_file(url: str, sha256: str, size_bytes: Optional[int],
                      dest: str, limits: ResourceLimits) -> int:
    req = urllib.request.Request(url, headers={"User-Agent": "agent-runtime/1"})
    try:
        resp = urllib.request.urlopen(req, timeout=limits.download_timeout)
    except urllib.error.HTTPError as exc:
        raise ResourceError(ERR_DOWNLOAD_FAILED, f"HTTP {exc.code}")
    except urllib.error.URLError as exc:
        raise ResourceError(ERR_DOWNLOAD_FAILED, f"unreachable: {exc.reason}")
    except TimeoutError:
        raise ResourceError(ERR_DOWNLOAD_FAILED, "timeout")
    except OSError as exc:
        raise ResourceError(ERR_DOWNLOAD_FAILED, str(exc))

    h = hashlib.sha256()
    total = 0
    with resp, open(dest, "wb") as fh:
        while True:
            try:
                chunk = resp.read(_CHUNK)
            except TimeoutError:
                raise ResourceError(ERR_DOWNLOAD_FAILED, "timeout during read")
            except OSError as exc:
                raise ResourceError(ERR_DOWNLOAD_FAILED, str(exc))
            if not chunk:
                break
            total += len(chunk)
            if total > limits.max_download_bytes:
                raise ResourceError(ERR_DOWNLOAD_TOO_LARGE, "download exceeds size bound")
            if size_bytes is not None and total > size_bytes:
                raise ResourceError(ERR_SIZE_MISMATCH,
                                    f"download exceeded declared size {size_bytes}")
            h.update(chunk)
            fh.write(chunk)
    if size_bytes is not None and total != size_bytes:
        raise ResourceError(ERR_SIZE_MISMATCH, f"size {total} != declared {size_bytes}")
    if h.hexdigest() != sha256:
        raise ResourceError(ERR_HASH_MISMATCH, "sha256 does not match declared hash")
    return total


def download_verified_bytes(url: str, *, sha256: str, size_bytes: Optional[int] = None,
                            limits: Optional[ResourceLimits] = None,
                            max_bytes: Optional[int] = None,
                            timeout: Optional[float] = None) -> Tuple[bytes, int]:
    lim = limits or ResourceLimits()
    if max_bytes is not None or timeout is not None:
        overrides: Dict[str, Any] = {}
        if max_bytes is not None:
            overrides["max_download_bytes"] = max_bytes
        if timeout is not None:
            overrides["download_timeout"] = timeout
        lim = ResourceLimits(**{**lim.__dict__, **overrides})
    fd, tmp = tempfile.mkstemp(prefix="agent-runtime-dl-")
    os.close(fd)
    try:
        size = _download_to_file(url, sha256, size_bytes, tmp, lim)
        with open(tmp, "rb") as fh:
            return fh.read(), size
    finally:
        try:
            os.unlink(tmp)
        except OSError:
            pass




def _detect_archive_ext(path: str) -> str:
    with open(path, "rb") as fh:
        magic = fh.read(4)
    if magic.startswith(b"PK\x03\x04") or magic.startswith(b"PK\x05\x06"):
        return ".zip"
    if magic.startswith(b"\x1f\x8b"):
        return ".tar.gz"
    raise ResourceError(ERR_SKILL_UNSUPPORTED_FORMAT,
                        f"skill archive format not supported (content detection)")


def _extract_skill_archive(archive_path: str, staging_root: str,
                           limits: ResourceLimits) -> str:
    with open(archive_path, "rb") as fh:
        magic = fh.read(4)
    if magic.startswith(b"PK\x03\x04") or magic.startswith(b"PK\x05\x06"):
        _extract_zip(archive_path, staging_root, limits)
    elif magic.startswith(b"\x1f\x8b"):
        _extract_targz(archive_path, staging_root, limits)
    else:
        raise ResourceError(ERR_SKILL_UNSUPPORTED_FORMAT,
                            "skill archive format not supported (content detection)")
    return _locate_skill_root(staging_root, limits)


def _extract_zip(path: str, staging: str, limits: ResourceLimits) -> None:
    seen = set()
    total = 0
    with zipfile.ZipFile(path) as zf:
        infos = zf.infolist()
        if len(infos) > limits.max_archive_entries:
            raise ResourceError(ERR_SKILL_LIMIT_EXCEEDED, "archive exceeds entry limit")
        declared = sum(i.file_size for i in infos if not i.is_dir())
        if declared > limits.max_unpacked_bytes:
            raise ResourceError(ERR_SKILL_LIMIT_EXCEEDED, "archive exceeds unpacked size limit")
        for info in infos:
            raw = info.filename
            is_dir = info.is_dir()
            rel = raw.rstrip("/") if is_dir else raw
            rel = validate_safe_relpath(rel, context="skill archive member")
            if rel in seen:
                raise ResourceError(ERR_SKILL_ARCHIVE_UNSAFE, f"duplicate member {raw!r}")
            seen.add(rel)
            if (info.external_attr >> 16) & 0o170000 == 0o120000:
                raise ResourceError(ERR_SKILL_ARCHIVE_UNSAFE, f"symlink not allowed: {raw!r}")
            dest = os.path.join(staging, rel)
            if is_dir:
                os.makedirs(dest, exist_ok=True)
                continue
            if info.file_size > limits.max_member_bytes:
                raise ResourceError(ERR_SKILL_LIMIT_EXCEEDED, f"member too large: {raw!r}")
            os.makedirs(os.path.dirname(dest), exist_ok=True)
            with zf.open(info) as src, open(dest, "wb") as out:
                while True:
                    chunk = src.read(_CHUNK)
                    if not chunk:
                        break
                    total += len(chunk)
                    if total > limits.max_unpacked_bytes:
                        raise ResourceError(ERR_SKILL_LIMIT_EXCEEDED,
                                            "archive exceeds unpacked size limit")
                    out.write(chunk)


def _extract_targz(path: str, staging: str, limits: ResourceLimits) -> None:
    try:
        tar = tarfile.open(path, "r:gz")
    except tarfile.TarError as exc:
        raise ResourceError(ERR_SKILL_UNSUPPORTED_FORMAT, f"not a valid tar.gz: {exc}")
    with tar:
        members = tar.getmembers()
        if len(members) > limits.max_archive_entries:
            raise ResourceError(ERR_SKILL_LIMIT_EXCEEDED, "archive exceeds entry limit")
        declared = sum(m.size for m in members if m.isfile())
        if declared > limits.max_unpacked_bytes:
            raise ResourceError(ERR_SKILL_LIMIT_EXCEEDED, "archive exceeds unpacked size limit")
        seen = set()
        total = 0
        for m in members:
            rel = validate_safe_relpath(m.name, context="skill archive member")
            if rel in seen:
                raise ResourceError(ERR_SKILL_ARCHIVE_UNSAFE, f"duplicate member {m.name!r}")
            seen.add(rel)
            if m.islnk() or m.issym() or not (m.isfile() or m.isdir()):
                raise ResourceError(ERR_SKILL_ARCHIVE_UNSAFE,
                                    f"special member not allowed: {m.name!r}")
            dest = os.path.join(staging, rel)
            if m.isdir():
                os.makedirs(dest, exist_ok=True)
                continue
            if m.size > limits.max_member_bytes:
                raise ResourceError(ERR_SKILL_LIMIT_EXCEEDED, f"member too large: {m.name!r}")
            os.makedirs(os.path.dirname(dest), exist_ok=True)
            src = tar.extractfile(m)
            with open(dest, "wb") as out:
                while True:
                    chunk = src.read(_CHUNK) if src else b""
                    if not chunk:
                        break
                    total += len(chunk)
                    if total > limits.max_unpacked_bytes:
                        raise ResourceError(ERR_SKILL_LIMIT_EXCEEDED,
                                            "archive exceeds unpacked size limit")
                    out.write(chunk)


def _locate_skill_root(staging: str, limits: ResourceLimits) -> str:
    root = os.path.join(staging, "SKILL.md")
    if os.path.isfile(root):
        skill_root = staging
    else:
        entries = [e for e in os.listdir(staging) if e != ".DS_Store"]
        if len(entries) == 1:
            solo = os.path.join(staging, entries[0])
            if os.path.isdir(solo) and os.path.isfile(os.path.join(solo, "SKILL.md")):
                skill_root = solo
            else:
                skill_root = None
        else:
            skill_root = None
    if skill_root is None:
        raise ResourceError(ERR_SKILL_MISSING_MANIFEST,
                            "no SKILL.md at skill root")
    md = os.path.join(skill_root, "SKILL.md")
    if os.path.getsize(md) > limits.max_skill_md_bytes:
        raise ResourceError(ERR_SKILL_LIMIT_EXCEEDED, "SKILL.md exceeds size limit")
    return skill_root


def _wipe_dir(path: str) -> None:
    if os.path.isdir(path):
        shutil.rmtree(path, ignore_errors=True)
    os.makedirs(path, exist_ok=True)


def _promote_skill(skill_root: str, final_dir: str) -> None:
    if os.path.isdir(final_dir):
        shutil.rmtree(final_dir, ignore_errors=True)
    os.makedirs(os.path.dirname(final_dir), exist_ok=True)
    shutil.copytree(skill_root, final_dir)


def _retain_archive(archive_path: str, workspace_dir: str, name: str,
                    ext: str, sha256: str) -> None:
    arch_dir = os.path.join(workspace_dir, ".skill-archives")
    os.makedirs(arch_dir, exist_ok=True)
    dest = os.path.join(arch_dir, f"{name}{ext}")
    if os.path.exists(dest) and _sha256_file(dest) == sha256:
        return
    shutil.copyfile(archive_path, dest)


def _sha256_file(path: str) -> str:
    h = hashlib.sha256()
    with open(path, "rb") as fh:
        while True:
            chunk = fh.read(_CHUNK)
            if not chunk:
                break
            h.update(chunk)
    return h.hexdigest()




class ResourceAdapter:

    def __init__(self, limits: Optional[ResourceLimits] = None):
        self.limits = limits or ResourceLimits()

    def prepare_files(self, files: List[Dict[str, Any]], dest: str) -> Dict[str, Dict[str, Any]]:
        os.makedirs(dest, exist_ok=True)
        out: Dict[str, Dict[str, Any]] = {}
        seen = set()
        for entry in files or []:
            if not isinstance(entry, dict):
                raise ResourceError(ERR_INVALID_ENTRY, "file entry must be an object")
            name = validate_safe_relpath(entry.get("name") or "", context="file name")
            if name in seen:
                raise ResourceError(ERR_DUPLICATE_PATH, f"duplicate file target {name!r}")
            seen.add(name)
            url, sha = entry.get("download_url"), entry.get("sha256")
            size = entry.get("size_bytes")
            if not url or not sha:
                raise ResourceError(ERR_INVALID_ENTRY,
                                    f"file {name!r} requires download_url and sha256")
            _validate_download_url(url)
            target = os.path.join(dest, name)
            os.makedirs(os.path.dirname(target) or dest, exist_ok=True)
            tmp = target + ".part"
            try:
                actual = _download_to_file(url, sha, size, tmp, self.limits)
                os.replace(tmp, target)
            except Exception:
                try:
                    os.unlink(tmp)
                except OSError:
                    pass
                raise
            out[name] = {"sha256": sha, "size_bytes": actual}
        return out

    def prepare_skills(self, skills: List[Dict[str, Any]], dest: str) -> List[Dict[str, Any]]:
        os.makedirs(dest, exist_ok=True)
        out: List[Dict[str, Any]] = []
        seen = set()
        for entry in skills or []:
            if not isinstance(entry, dict):
                raise ResourceError(ERR_INVALID_ENTRY, "skill entry must be an object")
            name = validate_skill_name(entry.get("name") or "")
            if name in seen:
                raise ResourceError(ERR_DUPLICATE_PATH, f"duplicate skill name {name!r}")
            seen.add(name)
            url, sha = entry.get("download_url"), entry.get("sha256")
            size = entry.get("size_bytes")
            if not url or not sha:
                raise ResourceError(ERR_INVALID_ENTRY,
                                    f"skill {name!r} requires download_url and sha256")
            _validate_download_url(url)
            staging = tempfile.mkdtemp(prefix="agent-runtime-skill-")
            try:
                archive_path = os.path.join(staging, "archive.bin")
                actual = _download_to_file(url, sha, size, archive_path, self.limits)
                if actual > self.limits.max_archive_bytes:
                    raise ResourceError(ERR_SKILL_LIMIT_EXCEEDED,
                                        f"skill {name!r} archive exceeds size bound")
                ext = _detect_archive_ext(archive_path)
                skill_root = _extract_skill_archive(
                    archive_path, os.path.join(staging, "root"), self.limits)
                final_dir = os.path.join(dest, ".skills", name)
                _promote_skill(skill_root, final_dir)
                _retain_archive(archive_path, dest, name, ext, sha)
                out.append({
                    "name": name,
                    "sha256": sha,
                    "size_bytes": actual,
                    "skill_dir": f".skills/{name}",
                    "archive_relpath": f".skill-archives/{name}{ext}",
                })
            finally:
                shutil.rmtree(staging, ignore_errors=True)
        return out

    def rebuild_skills(self, skills: List[Dict[str, Any]],
                       workspace_dir: str) -> List[Dict[str, Any]]:
        out: List[Dict[str, Any]] = []
        for entry in skills or []:
            name = validate_skill_name(entry.get("name") or "")
            sha = entry.get("sha256")
            if not sha:
                raise ResourceError(ERR_INVALID_ENTRY, f"skill {name!r} requires sha256")
            arch_dir = os.path.join(workspace_dir, ".skill-archives")
            match = None
            for cand in sorted(glob.glob(os.path.join(arch_dir, name + ".*"))):
                if _sha256_file(cand) == sha:
                    match = cand
                    break
            if match is None:
                raise ResourceError(ERR_SKILL_REBUILD_FAILED,
                                    f"no verified archive for skill {name!r}")
            staging = tempfile.mkdtemp(prefix="agent-runtime-skill-rebuild-")
            try:
                skill_root = _extract_skill_archive(
                    match, os.path.join(staging, "root"), self.limits)
                final_dir = os.path.join(workspace_dir, ".skills", name)
                _promote_skill(skill_root, final_dir)
                ext = ".zip" if match.endswith(".zip") else ".tar.gz"
                out.append({
                    "name": name,
                    "sha256": sha,
                    "size_bytes": os.path.getsize(match),
                    "skill_dir": f".skills/{name}",
                    "archive_relpath": f".skill-archives/{name}{ext}",
                })
            finally:
                shutil.rmtree(staging, ignore_errors=True)
        return out

    def _validate_entries(self, files: List[Dict[str, Any]],
                          skills: List[Dict[str, Any]]) -> None:
        for entry in files or []:
            if not isinstance(entry, dict):
                raise ResourceError(ERR_INVALID_ENTRY, "file entry must be an object")
            validate_safe_relpath(entry.get("name") or "", context="file name")
            if not entry.get("download_url") or not entry.get("sha256"):
                raise ResourceError(ERR_INVALID_ENTRY, "file entry requires download_url and sha256")
        seen = set()
        for entry in skills or []:
            name = validate_skill_name(entry.get("name") or "")
            if name in seen:
                raise ResourceError(ERR_DUPLICATE_PATH, f"duplicate skill name {name!r}")
            seen.add(name)
            if not entry.get("download_url") or not entry.get("sha256"):
                raise ResourceError(ERR_INVALID_ENTRY, "skill entry requires download_url and sha256")




def prepare_run_resources(config: Dict[str, Any], workspace_dir: str,
                          limits: Optional[ResourceLimits] = None) -> Dict[str, Any]:
    adapter = ResourceAdapter(limits=limits)
    files = config.get("files") or []
    skills = config.get("skills") or []
    if config.get("resume"):
        adapter._validate_entries(files, skills)
        return {"mode": "resume", "files": {}, "skills": []}
    file_meta = adapter.prepare_files(files, workspace_dir) if files else {}
    skill_meta = adapter.prepare_skills(skills, workspace_dir) if skills else []
    return {"mode": "fresh", "files": file_meta, "skills": skill_meta}


def collect_injectable_text(files: List[Dict[str, Any]], workspace_dir: str,
                            max_bytes: int = 60 * 1024) -> List[tuple[str, str]]:
    out: List[tuple[str, str]] = []
    for entry in files or []:
        if not isinstance(entry, dict):
            continue
        name = entry.get("name")
        if not name or name.startswith("/") or ".." in name:
            continue
        target = os.path.join(workspace_dir, name)
        try:
            with open(target, "rb") as fh:
                data = fh.read(max_bytes + 1)
        except OSError:
            continue
        if len(data) > max_bytes:
            data = data[:max_bytes]
        if b"\x00" in data:
            continue
        try:
            text = data.decode("utf-8")
        except UnicodeDecodeError:
            try:
                text = data.decode("utf-8", "replace")
            except Exception:  # noqa: BLE001
                continue
        if not text.strip():
            continue
        out.append((name, text))
    return out


__all__ = [
    "ResourceError", "ResourceLimits", "ResourceAdapter", "prepare_run_resources",
    "collect_injectable_text", "download_verified_bytes", "validate_safe_relpath",
    "validate_skill_name",
    "ERR_DOWNLOAD_FAILED", "ERR_DOWNLOAD_TOO_LARGE", "ERR_SIZE_MISMATCH",
    "ERR_HASH_MISMATCH", "ERR_UNSAFE_PATH", "ERR_DUPLICATE_PATH",
    "ERR_INVALID_ENTRY", "ERR_SKILL_UNSUPPORTED_FORMAT", "ERR_SKILL_ARCHIVE_UNSAFE",
    "ERR_SKILL_LIMIT_EXCEEDED", "ERR_SKILL_MISSING_MANIFEST", "ERR_SKILL_REBUILD_FAILED",
]
