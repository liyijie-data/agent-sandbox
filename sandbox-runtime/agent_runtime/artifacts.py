from __future__ import annotations

import hashlib
import io
import json
import os
import tempfile
import zipfile
from dataclasses import dataclass
from typing import Any, Dict, List, Optional, Tuple

from . import constants as C

_PROTOCOL_OUTPUT_FILES = frozenset({"result.json", "checkpoint.tar.gz"})
_PROTOCOL_PREFIXES = (".skill-archives", ".skills", ".skill")


class ArtifactDeliveryError(Exception):

    def __init__(self, code: str, reason: str):
        super().__init__(f"{code}: {reason}")
        self.code = code
        self.reason = reason


@dataclass
class ArtifactBundle:

    zip_path: str
    sha256: str
    size_bytes: int


def _walk_files(root: str) -> List[str]:
    out: List[str] = []
    for dirpath, dirnames, filenames in os.walk(root):
        dirnames.sort()
        for fn in sorted(filenames):
            full = os.path.join(dirpath, fn)
            out.append(os.path.relpath(full, root).replace(os.sep, "/"))
    return out


def _sha256_file(path: str) -> str:
    h = hashlib.sha256()
    with open(path, "rb") as fh:
        while True:
            chunk = fh.read(64 * 1024)
            if not chunk:
                break
            h.update(chunk)
    return h.hexdigest()


class ArtifactBundler:

    def __init__(self, workspace_dir: str, output_dir: str,
                 result_bundle: Optional[Dict[str, Any]] = None,
                 max_bundle_bytes: int = 64 * 1024 * 1024,
                 timeout: float = 60.0):
        self.workspace_dir = workspace_dir
        self.output_dir = output_dir
        self.result_bundle = dict(result_bundle or {})
        self.max_bundle_bytes = max_bundle_bytes
        self.timeout = timeout

    @property
    def destination_id(self) -> Optional[str]:
        return self.result_bundle.get("destination_id")

    @property
    def upload_url(self) -> Optional[str]:
        return self.result_bundle.get("upload_url")

    def collect(self, baseline: Any, workspace_dir: str,
                output_dir: str) -> Optional[ArtifactBundle]:
        entries = self._collect_entries(baseline or {}, workspace_dir, output_dir)
        if not entries:
            return None
        if not self.upload_url:
            raise ArtifactDeliveryError(
                C.ERR_ARTIFACT_DESTINATION_REQUIRED,
                "artifacts produced but no result_bundle destination declared")
        return self._build_bundle(entries)

    def _collect_entries(self, baseline: Dict[str, Any], workspace_dir: str,
                         output_dir: str) -> List[Tuple[str, str, str]]:
        baseline_ws: Dict[str, str] = (baseline or {}).get("workspace") or {}
        entries: List[Tuple[str, str, str]] = []
        if os.path.isdir(output_dir):
            for rel in _walk_files(output_dir):
                if rel in _PROTOCOL_OUTPUT_FILES or rel.startswith(_PROTOCOL_PREFIXES):
                    continue
                entries.append(("output", rel, os.path.join(output_dir, rel)))
        if os.path.isdir(workspace_dir):
            for rel in _walk_files(workspace_dir):
                if rel.startswith(_PROTOCOL_PREFIXES):
                    continue
                full = os.path.join(workspace_dir, rel)
                try:
                    digest = _sha256_file(full)
                except OSError:
                    continue
                if baseline_ws.get(rel) == digest:
                    continue
                entries.append(("workspace", rel, full))
        entries.sort(key=lambda e: (e[0], e[1]))
        return entries

    def _build_bundle(self, entries: List[Tuple[str, str, str]]) -> ArtifactBundle:
        buf = io.BytesIO()
        manifest: Dict[str, Any] = {"artifacts": []}
        total = 0
        with zipfile.ZipFile(buf, "w", zipfile.ZIP_DEFLATED) as zf:
            for source, name, full in entries:
                try:
                    with open(full, "rb") as fh:
                        data = fh.read()
                except OSError as exc:
                    raise ArtifactDeliveryError(
                        C.ERR_RESULT_BUNDLE_UPLOAD_FAILED, f"read {name}: {exc}")
                if len(data) > self.max_bundle_bytes:
                    raise ArtifactDeliveryError(
                        C.ERR_RESOURCE_LIMIT_EXCEEDED,
                        f"artifact {name} exceeds bundle bound")
                total += len(data)
                if total > self.max_bundle_bytes:
                    raise ArtifactDeliveryError(
                        C.ERR_RESOURCE_LIMIT_EXCEEDED,
                        "bundle exceeds total size bound")
                member = ("artifacts" if source == "output" else "workspace") + "/" + name
                zf.writestr(member, data)
                manifest["artifacts"].append({
                    "source": source,
                    "name": name,
                    "sha256": hashlib.sha256(data).hexdigest(),
                    "size_bytes": len(data),
                })
            zf.writestr("manifest.json", json.dumps(manifest, ensure_ascii=False))
        blob = buf.getvalue()
        fd, tmp = tempfile.mkstemp(prefix="agent-result-", suffix=".zip")
        os.close(fd)
        try:
            with open(tmp, "wb") as fh:
                fh.write(blob)
        except OSError:
            try:
                os.unlink(tmp)
            except OSError:
                pass
            raise
        return ArtifactBundle(tmp, hashlib.sha256(blob).hexdigest(), len(blob))

    def upload(self, bundle: ArtifactBundle) -> None:
        url = self.upload_url or ""
        try:
            from .tools.httpio import HttpTransportError, request_bytes
            with open(bundle.zip_path, "rb") as fh:
                payload = fh.read()
            status, _headers, _body = request_bytes(
                url, method="PUT",
                headers={"Content-Type": "application/zip"},
                body=payload, timeout=self.timeout,
                max_bytes=1024 * 1024)
            if not (200 <= status < 300):
                raise ArtifactDeliveryError(
                    C.ERR_RESULT_BUNDLE_UPLOAD_FAILED, f"upload HTTP {status}")
        except HttpTransportError as exc:
            raise ArtifactDeliveryError(
                C.ERR_RESULT_BUNDLE_UPLOAD_FAILED, f"upload failed: {exc.reason}")
        except OSError as exc:
            raise ArtifactDeliveryError(
                C.ERR_RESULT_BUNDLE_UPLOAD_FAILED, f"upload read failed: {exc}")
        finally:
            try:
                os.unlink(bundle.zip_path)
            except OSError:
                pass


__all__ = ["ArtifactBundler", "ArtifactBundle", "ArtifactDeliveryError"]
