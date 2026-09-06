# Agent Sandbox official reference Runtime image (T04).
#
# The reference Runtime is stdlib-only Python: DES-04 transport (http.server on
# 8888), model/steering clients (urllib), recovery package (tarfile), strict
# JSON contracts. No extension SDK is installed here; T06/T07 wire their own
# adapters and extend this image.

FROM python:3.12-slim

WORKDIR /app

RUN apt-get update && apt-get install -y --no-install-recommends curl \
    && rm -rf /var/lib/apt/lists/*

COPY requirements.txt /tmp/requirements.txt
RUN pip install --no-cache-dir -r /tmp/requirements.txt || true

COPY agent_runtime ./agent_runtime
COPY runtime_manifest.v1.json ./manifest.json
RUN mkdir -p /app/input /app/output /app/workspace

ENV PYTHONUNBUFFERED=1
ENV AGENT_RUNTIME_MANIFEST=/app/manifest.json

# Container start: run the DES-04 single-entry transport server. The Worker
# drives executions through POST /execute, which launches the registered
# manifest entrypoint (python -m agent_runtime ... --config /app/run.json).
CMD ["python", "-m", "agent_runtime", "--serve", "--port", "8888"]