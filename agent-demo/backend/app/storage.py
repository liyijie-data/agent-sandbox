from __future__ import annotations

import io
from datetime import timedelta
from pathlib import Path
from urllib.parse import urlsplit
from minio import Minio

from . import config


class ObjectStorage:
    def __init__(self) -> None:
        self.client = Minio(config.S3_ENDPOINT, access_key=config.S3_ACCESS_KEY_ID, secret_key=config.S3_SECRET_ACCESS_KEY, secure=config.S3_USE_SSL)
        self._public_client: Minio | None = None

    def init(self) -> None:
        if not self.client.bucket_exists(config.S3_BUCKET): self.client.make_bucket(config.S3_BUCKET)

    def put_url(self, key: str) -> str:
        return self._presign_client().presigned_put_object(config.S3_BUCKET, key, expires=timedelta(seconds=config.PRESIGN_TTL_SECONDS))

    def sandbox_put_url(self, key: str) -> str:
        return self._sandbox_client().presigned_put_object(config.S3_BUCKET, key, expires=timedelta(seconds=config.PRESIGN_TTL_SECONDS))

    def get_url(self, key: str) -> str:
        return self._presign_client().presigned_get_object(config.S3_BUCKET, key, expires=timedelta(seconds=config.PRESIGN_TTL_SECONDS))

    def sandbox_get_url(self, key: str) -> str:
        return self._sandbox_client().presigned_get_object(config.S3_BUCKET, key, expires=timedelta(seconds=config.PRESIGN_TTL_SECONDS))

    def download_url(self, key: str, filename: str) -> str:
        safe = Path(filename).name.replace('"', "_").replace("\\r", "_").replace("\\n", "_") or "download"
        return self._presign_client().presigned_get_object(config.S3_BUCKET, key, expires=timedelta(seconds=config.PRESIGN_TTL_SECONDS), response_headers={"response-content-disposition": f'attachment; filename="{safe}"'})

    def _presign_client(self) -> Minio:
        return self._client_for(config.S3_PUBLIC_ENDPOINT)

    def _sandbox_client(self) -> Minio:
        return self._client_for(config.S3_SANDBOX_ENDPOINT)

    def _client_for(self, configured_endpoint: str) -> Minio:
        endpoint = configured_endpoint or config.S3_ENDPOINT
        if "://" not in endpoint:
            endpoint = ("https://" if config.S3_USE_SSL else "http://") + endpoint
        public = urlsplit(endpoint)
        public_endpoint = public.netloc
        cache = self._public_client
        if cache is None or cache._base_url.host != public_endpoint:
            cache = Minio(
                public_endpoint,
                access_key=config.S3_ACCESS_KEY_ID,
                secret_key=config.S3_SECRET_ACCESS_KEY,
                secure=endpoint.startswith("https://"),
                region=self.client._get_region(config.S3_BUCKET),
            )
        if configured_endpoint == config.S3_PUBLIC_ENDPOINT:
            self._public_client = cache
        return cache

    def stat(self, key: str): return self.client.stat_object(config.S3_BUCKET, key)

    def get_bytes(self, key: str) -> bytes:
        response = self.client.get_object(config.S3_BUCKET, key)
        try:
            return response.read()
        finally:
            response.close()
            response.release_conn()

    def put_bytes(self, key: str, value: bytes, content_type: str) -> None:
        self.client.put_object(config.S3_BUCKET, key, io.BytesIO(value), len(value), content_type=content_type)
