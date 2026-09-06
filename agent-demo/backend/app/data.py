from __future__ import annotations

import json
from contextlib import contextmanager
from typing import Any, Iterator
from urllib.parse import urlparse

import pymysql

from . import config


class Store:
    def __init__(self) -> None:
        if not config.MYSQL_DSN:
            raise RuntimeError("MYSQL_DSN is required")
        parsed = urlparse(config.MYSQL_DSN)
        if parsed.scheme not in {"mysql", "mysql+pymysql"} or not parsed.hostname or not parsed.path:
            raise RuntimeError("MYSQL_DSN must be mysql://user:password@host:3306/database")
        self.options = {"host": parsed.hostname, "port": parsed.port or 3306, "user": parsed.username or "", "password": parsed.password or "", "database": parsed.path.lstrip("/"), "charset": "utf8mb4", "cursorclass": pymysql.cursors.DictCursor, "autocommit": False}

    @contextmanager
    def connection(self) -> Iterator[Any]:
        conn = pymysql.connect(**self.options)
        try:
            yield conn
            conn.commit()
        except Exception:
            conn.rollback()
            raise
        finally:
            conn.close()

    def init(self) -> None:
        schema = """
        CREATE TABLE IF NOT EXISTS demo_users (id CHAR(36) PRIMARY KEY, username VARCHAR(128) NOT NULL UNIQUE, password_hash CHAR(64) NOT NULL, password_salt CHAR(64) NOT NULL, created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP);
        CREATE TABLE IF NOT EXISTS demo_conversations (id CHAR(36) PRIMARY KEY, user_id CHAR(36) NOT NULL, title VARCHAR(256) NOT NULL, created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP, updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP, INDEX(user_id, updated_at));
        CREATE TABLE IF NOT EXISTS demo_messages (id CHAR(36) PRIMARY KEY, conversation_id CHAR(36) NOT NULL, run_id VARCHAR(128), role VARCHAR(16) NOT NULL, content MEDIUMTEXT NOT NULL, reasoning_content MEDIUMTEXT, attachments_json JSON, seq INT NOT NULL, created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP, UNIQUE KEY conversation_seq (conversation_id, seq));
        CREATE TABLE IF NOT EXISTS demo_resources (id CHAR(36) PRIMARY KEY, user_id CHAR(36) NOT NULL, kind VARCHAR(16) NOT NULL, name VARCHAR(512) NOT NULL, version VARCHAR(128), object_key VARCHAR(1024) NOT NULL, sha256 CHAR(64) NOT NULL, size_bytes BIGINT NOT NULL, status VARCHAR(16) NOT NULL, created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP, INDEX(user_id, kind, status));
        CREATE TABLE IF NOT EXISTS demo_tools (id CHAR(36) PRIMARY KEY, user_id CHAR(36) NOT NULL, type VARCHAR(16) NOT NULL, name VARCHAR(128) NOT NULL, endpoint VARCHAR(2048) NOT NULL, spec_resource_id CHAR(36), allowed_operations JSON, auth_json JSON, enabled TINYINT(1) NOT NULL DEFAULT 1, created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP, INDEX(user_id));
        CREATE TABLE IF NOT EXISTS demo_runs (id CHAR(36) PRIMARY KEY, user_id CHAR(36) NOT NULL, conversation_id CHAR(36) NOT NULL, req_id VARCHAR(128) NOT NULL UNIQUE, platform_run_id VARCHAR(128) UNIQUE, trace_id VARCHAR(128), status VARCHAR(32) NOT NULL, prompt MEDIUMTEXT NOT NULL, result_json JSON, last_event_id VARCHAR(64), created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP, updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP, INDEX(user_id, conversation_id, updated_at));
        CREATE TABLE IF NOT EXISTS demo_artifacts (id CHAR(36) PRIMARY KEY, run_id CHAR(36) NOT NULL, platform_run_id VARCHAR(128), name VARCHAR(1024) NOT NULL, content_type VARCHAR(256), object_key VARCHAR(1024) NOT NULL, sha256 CHAR(64) NOT NULL, dedupe_key CHAR(64) NOT NULL, size_bytes BIGINT NOT NULL, status VARCHAR(16) NOT NULL, created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP, INDEX(run_id, status), UNIQUE KEY run_artifact_dedupe (dedupe_key));
        """
        with self.connection() as conn:
            with conn.cursor() as cur:
                for statement in schema.split(";\n"):
                    if statement.strip(): cur.execute(statement)
                try:
                    cur.execute("ALTER TABLE demo_messages ADD COLUMN reasoning_content MEDIUMTEXT NULL")
                except pymysql.err.OperationalError as exc:
                    if exc.args[0] != 1060: raise
                try:
                    cur.execute("ALTER TABLE demo_messages ADD COLUMN attachments_json JSON NULL")
                except pymysql.err.OperationalError as exc:
                    if exc.args[0] != 1060: raise
                try:
                    cur.execute("ALTER TABLE demo_artifacts ADD COLUMN dedupe_key CHAR(64) NULL")
                except pymysql.err.OperationalError as exc:
                    if exc.args[0] != 1060: raise
                try:
                    cur.execute("ALTER TABLE demo_tools ADD COLUMN enabled TINYINT(1) NOT NULL DEFAULT 1")
                except pymysql.err.OperationalError as exc:
                    if exc.args[0] != 1060: raise
                cur.execute("UPDATE demo_artifacts SET dedupe_key=SHA2(CONCAT(run_id,0x00,object_key),256) WHERE dedupe_key IS NULL OR dedupe_key='' ")
                cur.execute("DELETE older FROM demo_artifacts older JOIN demo_artifacts canonical ON older.dedupe_key=canonical.dedupe_key AND older.id>canonical.id")
                try:
                    cur.execute("ALTER TABLE demo_artifacts MODIFY dedupe_key CHAR(64) NOT NULL")
                except pymysql.err.OperationalError as exc:
                    if exc.args[0] != 1060: raise
                for index in ("run_artifact", "run_artifact_object"):
                    try:
                        cur.execute(f"ALTER TABLE demo_artifacts DROP INDEX {index}")
                    except pymysql.err.OperationalError as exc:
                        if exc.args[0] != 1091: raise
                try:
                    cur.execute("ALTER TABLE demo_artifacts ADD UNIQUE KEY run_artifact_dedupe (dedupe_key)")
                except pymysql.err.OperationalError as exc:
                    if exc.args[0] != 1061: raise

    def one(self, sql: str, args: tuple = ()) -> dict[str, Any] | None:
        with self.connection() as conn:
            with conn.cursor() as cur:
                cur.execute(sql, args); return cur.fetchone()

    def many(self, sql: str, args: tuple = ()) -> list[dict[str, Any]]:
        with self.connection() as conn:
            with conn.cursor() as cur:
                cur.execute(sql, args); return list(cur.fetchall())

    def execute(self, sql: str, args: tuple = ()) -> int:
        with self.connection() as conn:
            with conn.cursor() as cur:
                return cur.execute(sql, args)

    @staticmethod
    def dump(value: Any) -> str: return json.dumps(value, ensure_ascii=False)
    @staticmethod
    def load(value: Any, fallback: Any) -> Any:
        if value is None: return fallback
        return json.loads(value) if isinstance(value, str) else value
