#!/usr/bin/env python3
from __future__ import annotations

import argparse
import hashlib
import hmac
import json
import os
import sys
import time
import uuid
from datetime import UTC, datetime, timedelta
from pathlib import Path
from typing import Any, Iterator
from urllib.error import HTTPError, URLError
from urllib.parse import quote, urlencode, urlsplit, urlunsplit
from urllib.request import Request, urlopen

DEFAULT_IMAGE_ID = "CHANGE_ME-runtime-image"
LOCAL_CONFIG = {
    "AGENT_PLATFORM_BASE_URL": "http://localhost:30080",
    "CLIENT_API_KEY": "<your-client-api-key>",
    "MODEL_NAME": "CHANGE_ME",
    "MODEL_BASE_URL": "https://model-upstream.example.com/v1",
    "MODEL_ACCESS_API_KEY": "<your-model-api-key>",
    "S3_ENDPOINT": "<s3-host>:9000",
    "S3_BUCKET": "CHANGE_ME",
    "S3_ACCESS_KEY_ID": "CHANGE_ME",
    "S3_SECRET_ACCESS_KEY": "CHANGE_ME",
    "S3_REGION": "us-east-1",
    "S3_USE_SSL": "false",
    "AGENT_PLATFORM_IMAGE_ID": DEFAULT_IMAGE_ID,
}


class APIError(RuntimeError):
    def __init__(self, status_code: int, body: bytes) -> None:
        self.status_code = status_code
        try:
            self.body: Any = json.loads(body.decode("utf-8"))
        except (UnicodeDecodeError, json.JSONDecodeError):
            self.body = {"error": {"message": body.decode("utf-8", "replace")[:500]}}
        super().__init__(f"HTTP {status_code}: {json.dumps(self.body, ensure_ascii=False)}")


def env(*names: str, required: bool = False) -> str | None:
    value = next((os.environ[n].strip() for n in names if os.environ.get(n, "").strip()), None)
    if not value:
        value = next((str(LOCAL_CONFIG.get(n, "")).strip() for n in names if str(LOCAL_CONFIG.get(n, "")).strip()), None)
    if required and not value:
        raise ValueError(f"missing required environment variable: {names[0]}")
    return value


def json_value(value: str) -> Any:
    try:
        return json.loads(value)
    except json.JSONDecodeError as exc:
        raise argparse.ArgumentTypeError(f"invalid JSON: {exc.msg}") from exc


def json_file(path: str) -> Any:
    try:
        return json.loads(Path(path).read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise argparse.ArgumentTypeError(f"cannot read JSON file {path}: {exc}") from exc


def print_json(value: Any) -> None:
    print(json.dumps(value, ensure_ascii=False, indent=2, sort_keys=True))


def request_bytes(method: str, url: str, *, headers: dict[str, str] | None = None, payload: Any | None = None, body: bytes | None = None, timeout: int = 30) -> bytes:
    if payload is not None and body is not None:
        raise ValueError("payload and body cannot both be set")
    data = json.dumps(payload, separators=(",", ":")).encode() if payload is not None else body
    try:
        with urlopen(Request(url, data=data, headers=headers or {}, method=method), timeout=timeout) as response:
            return response.read()
    except HTTPError as exc:
        raise APIError(exc.code, exc.read()) from exc


def request_json(method: str, url: str, *, headers: dict[str, str] | None = None, payload: Any | None = None, timeout: int = 30) -> Any:
    raw = request_bytes(method, url, headers=headers, payload=payload, timeout=timeout)
    return json.loads(raw.decode()) if raw else {}


class S3Client:

    def __init__(self, endpoint: str, bucket: str, access_key: str, secret_key: str, region: str, use_ssl: bool) -> None:
        if "://" not in endpoint:
            endpoint = ("https://" if use_ssl else "http://") + endpoint
        parsed = urlsplit(endpoint)
        if not parsed.scheme or not parsed.netloc:
            raise ValueError("S3_ENDPOINT must be an absolute URL or host:port")
        self.endpoint = parsed._replace(path=parsed.path.rstrip("/"), query="", fragment="")
        self.bucket, self.access_key, self.secret_key, self.region = bucket, access_key, secret_key, region

    @staticmethod
    def _sign(key: bytes, message: str) -> bytes:
        return hmac.new(key, message.encode("utf-8"), hashlib.sha256).digest()

    def presign(self, method: str, key: str, expires: int = 3600) -> str:
        if not key or key.startswith("/"):
            raise ValueError("S3 object key must be a non-empty relative path")
        now = datetime.now(UTC)
        date, timestamp = now.strftime("%Y%m%d"), now.strftime("%Y%m%dT%H%M%SZ")
        scope = f"{date}/{self.region}/s3/aws4_request"
        host = self.endpoint.netloc
        canonical_uri = quote(f"{self.endpoint.path}/{self.bucket}/{key}", safe="/-_.~")
        query = {
            "X-Amz-Algorithm": "AWS4-HMAC-SHA256",
            "X-Amz-Credential": f"{self.access_key}/{scope}",
            "X-Amz-Date": timestamp,
            "X-Amz-Expires": str(expires),
            "X-Amz-SignedHeaders": "host",
        }
        canonical_query = urlencode(sorted(query.items()), quote_via=quote, safe="-_.~")
        canonical_request = "\n".join((method, canonical_uri, canonical_query, f"host:{host}\n", "host", "UNSIGNED-PAYLOAD"))
        string_to_sign = "\n".join(("AWS4-HMAC-SHA256", timestamp, scope, hashlib.sha256(canonical_request.encode()).hexdigest()))
        signing_key = self._sign(self._sign(self._sign(self._sign(("AWS4" + self.secret_key).encode(), date), self.region), "s3"), "aws4_request")
        query["X-Amz-Signature"] = hmac.new(signing_key, string_to_sign.encode(), hashlib.sha256).hexdigest()
        return urlunsplit((self.endpoint.scheme, host, canonical_uri, urlencode(sorted(query.items()), quote_via=quote, safe="-_.~"), ""))

    def put_file(self, path: Path, key: str) -> dict[str, Any]:
        content = path.read_bytes()
        request_bytes("PUT", self.presign("PUT", key), body=content, timeout=60)
        return {"id": str(uuid.uuid4()), "name": path.name, "sha256": hashlib.sha256(content).hexdigest(), "size_bytes": len(content), "download_url": self.presign("GET", key), "expires_at": (datetime.now(UTC) + timedelta(hours=1)).isoformat()}


def s3_from_args(args: argparse.Namespace) -> S3Client:
    use_ssl = args.s3_use_ssl if args.s3_use_ssl is not None else env("S3_USE_SSL")
    return S3Client(
        args.s3_endpoint or env("S3_ENDPOINT", "S3_PUBLIC_ENDPOINT", required=True),
        args.s3_bucket or env("S3_BUCKET") or "agent-demo",
        args.s3_access_key_id or env("S3_ACCESS_KEY_ID", required=True),
        args.s3_secret_access_key or env("S3_SECRET_ACCESS_KEY", required=True),
        args.s3_region or env("S3_REGION") or "us-east-1",
        use_ssl is None or use_ssl.lower() not in {"0", "false", "no"},
    )


class PlatformClient:
    def __init__(self, base_url: str, api_key: str, timeout: int) -> None:
        self.base_url, self.timeout = base_url.rstrip("/"), timeout
        self.headers = {"Authorization": f"Bearer {api_key}", "Content-Type": "application/json"}

    def call(self, method: str, path: str, payload: Any | None = None) -> Any:
        return request_json(method, self.base_url + path, headers=self.headers, payload=payload, timeout=self.timeout)

    def create_run(self, payload: dict[str, Any]) -> Any: return self.call("POST", "/api/v1/runs", payload)
    def get_run(self, run_id: str) -> Any: return self.call("GET", f"/api/v1/runs/{run_id}")
    def get_steer(self, run_id: str, steer_id: str) -> Any: return self.call("GET", f"/api/v1/runs/{run_id}/steers/{steer_id}")
    def cancel(self, run_id: str) -> Any: return self.call("POST", f"/api/v1/runs/{run_id}/cancel", {})

    def answer(self, run_id: str, input_id: str, answer: Any, access_refresh: Any | None) -> Any:
        payload = {"answer": answer}
        if access_refresh is not None: payload["access_refresh"] = access_refresh
        return self.call("POST", f"/api/v1/runs/{run_id}/inputs/{input_id}/answer", payload)

    def steer(self, run_id: str, steer_id: str, message: Any) -> Any:
        if not isinstance(message, dict): message = {"role": "user", "content": message}
        return self.call("POST", f"/api/v1/runs/{run_id}/steers", {"steer_id": steer_id, "message": message})

    def get_network(self, image_id: str | None) -> Any:
        return self.call("GET", f"/api/v1/images/{image_id}/config/network" if image_id else "/api/v1/config/network")

    def put_network(self, config: dict[str, Any], image_id: str | None) -> Any:
        return self.call("PUT", f"/api/v1/images/{image_id}/config/network" if image_id else "/api/v1/config/network", config)

    def events(self, run_id: str, cursor: str | None, delay: float, max_reconnects: int) -> Iterator[dict[str, Any]]:
        retries = 0
        while True:
            headers = {**self.headers, "Accept": "text/event-stream"}
            if cursor: headers["Last-Event-ID"] = cursor
            try:
                with urlopen(Request(self.base_url + f"/api/v1/runs/{run_id}/events", headers=headers), timeout=self.timeout) as response:
                    frame: dict[str, Any] = {"event": "message", "data": []}
                    for raw in response:
                        line = raw.decode().rstrip("\r\n")
                        if not line:
                            if frame["data"]:
                                frame["data"] = "\n".join(frame["data"])
                                if frame.get("id"): cursor = str(frame["id"])
                                yield frame
                                if frame["data"] == "[DONE]": return
                            frame = {"event": "message", "data": []}
                        elif line.startswith(":"):
                            continue
                        elif ":" in line:
                            key, value = line.split(":", 1); value = value[1:] if value.startswith(" ") else value
                            if key == "data": frame["data"].append(value)
                            elif key in {"id", "event", "retry"}: frame[key] = value
                retries += 1
            except HTTPError as exc:
                raise APIError(exc.code, exc.read()) from exc
            except (URLError, OSError) as exc:
                retries += 1
                print(f"SSE disconnected ({type(exc).__name__}); reconnecting with Last-Event-ID={cursor or '<none>'}", file=sys.stderr)
            if max_reconnects >= 0 and retries > max_reconnects: raise RuntimeError("SSE reconnect limit reached")
            time.sleep(delay)


def platform(args: argparse.Namespace) -> PlatformClient:
    return PlatformClient(args.base_url or env("AGENT_PLATFORM_BASE_URL", "BASE_URL", required=True), args.client_api_key or env("CLIENT_API_KEY", required=True), args.timeout)


def create_payload(args: argparse.Namespace) -> dict[str, Any]:
    expires = args.model_access_expires_at or env("MODEL_ACCESS_EXPIRES_AT") or (datetime.now(UTC) + timedelta(minutes=15)).isoformat()
    payload: dict[str, Any] = {"req_id": args.req_id or f"example-{uuid.uuid4()}", "sandbox": {"image_id": args.image_id}, "messages": [{"role": "user", "content": args.prompt}], "model": {"name": args.model_name or env("MODEL_NAME", required=True), "base_url": args.model_base_url or env("MODEL_BASE_URL", "MODEL_UPSTREAM_BASE_URL", required=True), "access": {"api_key": args.model_access_api_key or env("MODEL_ACCESS_API_KEY", required=True), "expires_at": expires}}}
    if args.reasoning_effort: payload["model"]["reasoning_effort"] = args.reasoning_effort
    refs = args.resource_ref or []
    if not all(isinstance(ref, dict) for ref in refs): raise ValueError("each --resource-ref must be a JSON object")
    files = [{k: v for k, v in r.items() if k != "kind"} for r in refs if r.get("kind", "file") == "file"]
    skills = [{k: v for k, v in r.items() if k != "kind"} for r in refs if r.get("kind") == "skill"]
    if files: payload["files"] = files
    if skills: payload["skills"] = skills
    if args.tools: payload["tools"] = args.tools
    if args.result_bundle: payload["result_bundle"] = args.result_bundle
    return payload


def create(args: argparse.Namespace) -> None:
    print_json(platform(args).create_run(create_payload(args)))


def run(args: argparse.Namespace) -> None:
    client = platform(args)
    storage = s3_from_args(args)
    payload = create_payload(args)
    transfer_id = str(uuid.uuid4())
    for value in args.file or []:
        path = Path(value)
        ref = storage.put_file(path, f"agent-platform-client/inputs/{transfer_id}/files/{path.name}")
        payload.setdefault("files", []).append(ref)
    for value in args.skill or []:
        path = Path(value)
        if path.suffix.lower() != ".zip":
            raise ValueError(f"skill must be a .zip archive: {path}")
        ref = storage.put_file(path, f"agent-platform-client/inputs/{transfer_id}/skills/{path.name}")
        payload.setdefault("skills", []).append(ref)
    result_id = f"result-{uuid.uuid4()}"
    result_key = f"agent-platform-client/results/{result_id}.zip"
    payload["result_bundle"] = {
        "destination_id": result_id,
        "upload_url": storage.presign("PUT", result_key),
        "expires_at": (datetime.now(UTC) + timedelta(hours=1)).isoformat(),
    }
    created = client.create_run(payload)
    run_id = created["id"]
    print_json({"run": created, "next": "streaming events"})
    for frame in client.events(run_id, None, args.reconnect_delay, args.max_reconnects):
        print_json(frame)
        if frame.get("event") == "agent.request_input":
            print(
                "Run is awaiting input. Submit the displayed input_id with the answer command, then use events to resume streaming.",
                file=sys.stderr,
            )
            return
    final = client.get_run(run_id)
    print_json({"final": final})
    if final.get("status") != "succeeded":
        print("Run did not succeed; result bundle was not downloaded.", file=sys.stderr)
        return
    result = final.get("result") or {}
    if result.get("status") != "uploaded":
        print("Run succeeded without an uploaded result bundle.", file=sys.stderr)
        return
    content = request_bytes("GET", storage.presign("GET", result_key), timeout=60)
    actual_sha256 = hashlib.sha256(content).hexdigest()
    expected_sha256 = result.get("sha256")
    if not isinstance(expected_sha256, str) or actual_sha256 != expected_sha256:
        raise RuntimeError("downloaded result bundle SHA-256 does not match the Run result")
    output = Path(args.output_dir) / f"{run_id}.zip"
    output.parent.mkdir(parents=True, exist_ok=True)
    output.write_bytes(content)
    print(f"verified result bundle: {output}")


def event_stream(args: argparse.Namespace) -> None:
    for frame in platform(args).events(args.run_id, args.last_event_id, args.reconnect_delay, args.max_reconnects): print_json(frame)


def network_put(args: argparse.Namespace) -> None:
    config = args.config_file if args.config_file is not None else args.config
    if not isinstance(config, dict): raise ValueError("network config must be a JSON object")
    print_json(platform(args).put_network(config, args.image_id if args.image_scope else None))


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description="Standard-library Agent Platform V1 client")
    parser.add_argument("--base-url", help="platform URL; defaults to AGENT_PLATFORM_BASE_URL or BASE_URL"); parser.add_argument("--client-api-key", help="defaults to CLIENT_API_KEY"); parser.add_argument("--image-id", default=env("AGENT_PLATFORM_IMAGE_ID") or DEFAULT_IMAGE_ID, help=f"runtime image ID (default: {DEFAULT_IMAGE_ID})"); parser.add_argument("--timeout", type=int, default=30); parser.add_argument("--s3-endpoint", help="defaults to S3_ENDPOINT or S3_PUBLIC_ENDPOINT"); parser.add_argument("--s3-bucket", help="defaults to S3_BUCKET or agent-demo"); parser.add_argument("--s3-access-key-id", help="defaults to S3_ACCESS_KEY_ID"); parser.add_argument("--s3-secret-access-key", help="defaults to S3_SECRET_ACCESS_KEY"); parser.add_argument("--s3-region", help="defaults to S3_REGION or us-east-1"); parser.add_argument("--s3-use-ssl", choices=("true", "false"), help="defaults to S3_USE_SSL")
    commands = parser.add_subparsers(dest="command", required=True)
    def add_create_options(command: argparse.ArgumentParser) -> None:
        command.add_argument("--prompt", required=True); command.add_argument("--req-id"); command.add_argument("--model-name"); command.add_argument("--model-base-url"); command.add_argument("--model-access-api-key", help="defaults to MODEL_ACCESS_API_KEY"); command.add_argument("--model-access-expires-at"); command.add_argument("--reasoning-effort"); command.add_argument("--resource-ref", type=json_value, action="append", help='ResourceRef JSON; add "kind":"skill" for skills'); command.add_argument("--tool", type=json_value, dest="tools", action="append"); command.add_argument("--result-bundle", type=json_value)
    p = commands.add_parser("create", help="create a Run"); add_create_options(p); p.set_defaults(handler=create)
    p = commands.add_parser("run", help="create, stream, then verify/download a result bundle"); add_create_options(p); p.add_argument("--file", action="append", help="local input file; repeatable"); p.add_argument("--skill", action="append", help="local skill ZIP; repeatable"); p.add_argument("--output-dir", default="./agent-platform-output"); p.add_argument("--reconnect-delay", type=float, default=1.0); p.add_argument("--max-reconnects", type=int, default=-1); p.set_defaults(handler=run)
    p = commands.add_parser("get", help="query a Run"); p.add_argument("run_id"); p.set_defaults(handler=lambda a: print_json(platform(a).get_run(a.run_id)))
    p = commands.add_parser("events", help="stream SSE with Last-Event-ID reconnect"); p.add_argument("run_id"); p.add_argument("--last-event-id"); p.add_argument("--reconnect-delay", type=float, default=1.0); p.add_argument("--max-reconnects", type=int, default=-1); p.set_defaults(handler=event_stream)
    p = commands.add_parser("answer", help="answer agent.request_input"); p.add_argument("run_id"); p.add_argument("input_id"); p.add_argument("--answer", type=json_value, required=True); p.add_argument("--access-refresh", type=json_value); p.set_defaults(handler=lambda a: print_json(platform(a).answer(a.run_id, a.input_id, a.answer, a.access_refresh)))
    p = commands.add_parser("steer", help="send runtime guidance"); p.add_argument("run_id"); p.add_argument("--steer-id"); p.add_argument("--message", required=True); p.set_defaults(handler=lambda a: print_json(platform(a).steer(a.run_id, a.steer_id or f"steer-{uuid.uuid4()}", a.message)))
    p = commands.add_parser("get-steer", help="query a steer receipt"); p.add_argument("run_id"); p.add_argument("steer_id"); p.set_defaults(handler=lambda a: print_json(platform(a).get_steer(a.run_id, a.steer_id)))
    p = commands.add_parser("cancel", help="cancel a Run"); p.add_argument("run_id"); p.set_defaults(handler=lambda a: print_json(platform(a).cancel(a.run_id)))
    p = commands.add_parser("network-get", help="read global config, or image config with --image-scope"); p.add_argument("--image-scope", action="store_true"); p.set_defaults(handler=lambda a: print_json(platform(a).get_network(a.image_id if a.image_scope else None)))
    p = commands.add_parser("network-put", help="replace global config, or image override with --image-scope"); p.add_argument("--image-scope", action="store_true"); group = p.add_mutually_exclusive_group(required=True); group.add_argument("--config", type=json_value); group.add_argument("--config-file", type=json_file); p.set_defaults(handler=network_put)
    return parser


def main() -> int:
    args = build_parser().parse_args()
    try:
        args.handler(args); return 0
    except (ValueError, OSError, APIError, URLError, RuntimeError) as exc:
        print(f"error: {exc}", file=sys.stderr); return 1


if __name__ == "__main__":
    raise SystemExit(main())
