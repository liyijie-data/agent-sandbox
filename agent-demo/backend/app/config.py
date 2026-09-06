from __future__ import annotations

import os
import secrets
from pathlib import Path

from dotenv import load_dotenv

_env_file = os.environ.get("ENV_FILE") or str(Path(__file__).resolve().parents[1] / ".env")
load_dotenv(_env_file, override=False)

AGENT_PLATFORM_BASE_URL = os.environ.get(
    "AGENT_PLATFORM_BASE_URL", "http://127.0.0.1:30080"
).rstrip("/")

AGENT_PLATFORM_CLIENT_API_KEY = os.environ.get("AGENT_PLATFORM_CLIENT_API_KEY", "")
AGENT_PLATFORM_IMAGE_ID = os.environ.get("AGENT_PLATFORM_IMAGE_ID", "agent-platform-runtime-v1")
MODEL_PROVIDER = os.environ.get("MODEL_PROVIDER", "openai")
MODEL_NAME = os.environ.get("MODEL_NAME", "gpt-5")
MODEL_UPSTREAM_BASE_URL = os.environ.get("MODEL_UPSTREAM_BASE_URL", "").rstrip("/")
MODEL_UPSTREAM_API_KEY = os.environ.get("MODEL_UPSTREAM_API_KEY", "")
MYSQL_DSN = os.environ.get("MYSQL_DSN", "")
REDIS_URL = os.environ.get("REDIS_URL", "redis://127.0.0.1:6379/1")
REDIS_KEY_PREFIX = os.environ.get("REDIS_KEY_PREFIX", "agent-demo:")
S3_ENDPOINT = os.environ.get("S3_ENDPOINT", "127.0.0.1:9000")
S3_PUBLIC_ENDPOINT = os.environ.get("S3_PUBLIC_ENDPOINT", "").strip() or S3_ENDPOINT
S3_SANDBOX_ENDPOINT = os.environ.get("S3_SANDBOX_ENDPOINT", "").strip() or S3_PUBLIC_ENDPOINT
S3_BUCKET = os.environ.get("S3_BUCKET", "agent-demo")
S3_ACCESS_KEY_ID = os.environ.get("S3_ACCESS_KEY_ID", "")
S3_SECRET_ACCESS_KEY = os.environ.get("S3_SECRET_ACCESS_KEY", "")
S3_USE_SSL = os.environ.get("S3_USE_SSL", "false").lower() == "true"
PRESIGN_TTL_SECONDS = int(os.environ.get("PRESIGN_TTL_SECONDS", "900"))
SESSION_TTL_SECONDS = int(os.environ.get("SESSION_TTL_SECONDS", "86400"))
RUNTIME_TOKEN_TTL_SECONDS = int(os.environ.get("RUNTIME_TOKEN_TTL_SECONDS", "7200"))
MAX_UPLOAD_BYTES = int(os.environ.get("MAX_UPLOAD_BYTES", str(100 * 1024 * 1024)))


def token(prefix: str = "") -> str:
    return prefix + secrets.token_urlsafe(32)
