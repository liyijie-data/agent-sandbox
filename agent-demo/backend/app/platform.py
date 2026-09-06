from __future__ import annotations

import json
import logging
import sys
from typing import Any, AsyncIterator
import httpx

from . import config
from .logging_middleware import redact_value, truncate


logger = logging.getLogger("agent_demo.platform")
if not logger.handlers:
    _handler = logging.StreamHandler(sys.stdout)
    _handler.setFormatter(logging.Formatter("%(asctime)s %(levelname)s %(message)s"))
    logger.addHandler(_handler)
    logger.setLevel(logging.INFO)
    logger.propagate = False


class PlatformHTTPError(httpx.HTTPStatusError):

    def __init__(self, response: httpx.Response) -> None:
        super().__init__(f"platform returned HTTP {response.status_code}", request=response.request, response=response)
        try:
            self.body: Any = response.json()
        except Exception:
            self.body = None


class AgentPlatformClient:
    def __init__(self, base_url: str = config.AGENT_PLATFORM_BASE_URL) -> None:
        self.base_url = base_url

    def _headers(self) -> dict[str, str]:
        return {"Authorization": f"Bearer {config.AGENT_PLATFORM_CLIENT_API_KEY}", "Content-Type": "application/json"}

    @staticmethod
    def _log_body(value: Any) -> str:
        try:
            return truncate(json.dumps(redact_value(value), ensure_ascii=False))
        except (TypeError, ValueError):
            return truncate(repr(value))

    async def _request(self, method: str, path: str, **kwargs: Any) -> httpx.Response:
        url = f"{self.base_url}{path}"
        payload = kwargs.get("json")
        logger.info("platform request method=%s url=%s body=%s", method, url, self._log_body(payload) if payload is not None else "-")
        try:
            async with httpx.AsyncClient(timeout=kwargs.pop("timeout", 30)) as client:
                response = await client.request(method, url, headers=self._headers(), **kwargs)
        except httpx.HTTPError as exc:
            logger.warning("platform response method=%s url=%s transport_error=%s", method, url, exc)
            raise
        try:
            body: Any = response.json()
        except ValueError:
            body = response.text
        logger.info(
            "platform response method=%s url=%s status=%s trace_id=%s body=%s",
            method, url, response.status_code, response.headers.get("X-Trace-ID", ""), self._log_body(body),
        )
        return response

    @staticmethod
    def _json_result(response: httpx.Response) -> tuple[int, Any]:
        try:
            return response.status_code, response.json()
        except Exception:
            return response.status_code, None

    async def create_run(self, payload: dict[str, Any]) -> tuple[dict[str, Any], str]:
        response = await self._request("POST", "/api/v1/runs", json=payload)
        if response.is_error:
            raise PlatformHTTPError(response)
        return response.json(), response.headers.get("X-Trace-ID", "")

    async def get_run(self, run_id: str) -> dict[str, Any]:
        response = await self._request("GET", f"/api/v1/runs/{run_id}")
        response.raise_for_status()
        return response.json()

    async def get_events_stream(
        self, run_id: str, last_event_id: str | None = None
    ) -> AsyncIterator[str]:
        headers = {**self._headers(), "Accept": "text/event-stream"}
        if last_event_id:
            headers["Last-Event-ID"] = last_event_id
        url = f"{self.base_url}/api/v1/runs/{run_id}/events"
        logger.info("platform request method=GET url=%s body=-", url)
        async with httpx.AsyncClient(timeout=None) as client:
            async with client.stream("GET", url, headers=headers) as response:
                logger.info("platform response method=GET url=%s status=%s trace_id=%s body=<SSE stream>", url, response.status_code, response.headers.get("X-Trace-ID", ""))
                response.raise_for_status()
                async for line in response.aiter_lines():
                    yield line

    async def answer(
        self,
        run_id: str,
        input_id: str,
        answer: Any,
        access_refresh: dict[str, Any] | None = None,
    ) -> dict[str, Any]:
        body: dict[str, Any] = {"answer": answer}
        if access_refresh:
            body["access_refresh"] = access_refresh
        response = await self._request("POST", f"/api/v1/runs/{run_id}/inputs/{input_id}/answer", json=body)
        response.raise_for_status()
        return response.json()

    async def create_steer(
        self,
        run_id: str,
        steer_id: str,
        message: str | dict[str, Any],
    ) -> dict[str, Any]:
        body = {
            "steer_id": steer_id,
            "message": (
                message
                if isinstance(message, dict)
                else {"role": "user", "content": message}
            ),
        }
        response = await self._request("POST", f"/api/v1/runs/{run_id}/steers", json=body)
        response.raise_for_status()
        return response.json()

    async def get_steer(self, run_id: str, steer_id: str) -> dict[str, Any]:
        response = await self._request("GET", f"/api/v1/runs/{run_id}/steers/{steer_id}")
        response.raise_for_status()
        return response.json()

    async def cancel(self, run_id: str) -> dict[str, Any]:
        response = await self._request("POST", f"/api/v1/runs/{run_id}/cancel")
        response.raise_for_status()
        return response.json()


    async def get_client_network_config(self) -> tuple[int, Any]:
        return self._json_result(await self._request("GET", "/api/v1/config/network"))

    async def put_client_network_config(self, payload: dict[str, Any]) -> tuple[int, Any]:
        return self._json_result(await self._request("PUT", "/api/v1/config/network", json=payload))

    async def get_image_network_config(self, image_id: str) -> tuple[int, Any]:
        return self._json_result(await self._request("GET", f"/api/v1/images/{image_id}/config/network"))

    async def put_image_network_config(self, image_id: str, payload: dict[str, Any]) -> tuple[int, Any]:
        return self._json_result(await self._request("PUT", f"/api/v1/images/{image_id}/config/network", json=payload))
