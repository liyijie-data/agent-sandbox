import http from 'node:http';

const MAX_BODY = 4 * 1024 * 1024;

function sendJSON(res, status, value) {
  if (res.headersSent) return;
  const body = Buffer.from(JSON.stringify(value));
  res.writeHead(status, {
    'content-type': 'application/json',
    'content-length': body.length,
  });
  res.end(body);
}

function sendError(res, status, code) {
  sendJSON(res, status, {status: 'error', error_code: code});
}

async function readBody(req) {
  const declaredLength = Number(req.headers['content-length'] || 0);
  if (Number.isSafeInteger(declaredLength) && declaredLength > MAX_BODY) {
    req.resume();
    throw Object.assign(new Error('payload too large'), {status: 413});
  }
  const chunks = [];
  let size = 0;
  for await (const chunk of req) {
    size += chunk.length;
    if (size > MAX_BODY) {
      throw Object.assign(new Error('payload too large'), {status: 413});
    }
    chunks.push(chunk);
  }
  return Buffer.concat(chunks);
}

function parseJSON(raw) {
  const value = JSON.parse(raw.toString('utf8'));
  if (!value || typeof value !== 'object' || Array.isArray(value)) {
    throw new Error('invalid request');
  }
  return value;
}

function parseMultipart(raw, contentType) {
  const match = /boundary=(?:"([^"]+)"|([^;]+))/i.exec(contentType || '');
  if (!match) throw new Error('invalid multipart');
  const boundary = Buffer.from(`--${match[1] || match[2].trim()}`);
  const start = raw.indexOf(boundary);
  const headerEnd = raw.indexOf(Buffer.from('\r\n\r\n'), start + boundary.length);
  if (start < 0 || headerEnd < 0) throw new Error('invalid multipart');
  const headers = raw.subarray(start + boundary.length, headerEnd).toString('utf8');
  const disposition = /content-disposition:[^\r\n]*\bname="file"[^\r\n]*\bfilename="([^"]*)"/i.exec(
    headers,
  );
  const dataStart = headerEnd + 4;
  const next = raw.indexOf(Buffer.from(`\r\n${boundary.toString()}`), dataStart);
  if (!disposition || next < 0) throw new Error('invalid multipart');
  return {
    filename: disposition[1],
    data: raw.subarray(dataStart, next),
  };
}

async function health(_req, res, runtime) {
  sendJSON(res, 200, runtime.health());
}

async function manifest(_req, res, runtime) {
  sendJSON(res, 200, runtime.manifest);
}

async function execute(req, res, runtime) {
  const input = parseJSON(await readBody(req));
  if (!runtime.validCommand(input.command)) {
    return sendError(res, 400, 'invalid_request');
  }
  const id = input.execution_id || runtime.readExecutionId();
  if (typeof id !== 'string' || !id) return sendError(res, 400, 'invalid_request');
  try {
    const result = await runtime.execute(id);
    if (result.error) return sendError(res, 500, 'agent_execution_failed');
    return sendJSON(res, 200, result);
  } catch (error) {
    const status = error.code === 'runtime_state_conflict' ? 409 : 500;
    return sendError(res, status, error.code || 'agent_execution_failed');
  }
}

async function cancel(req, res, runtime) {
  const input = parseJSON(await readBody(req));
  if (typeof input.execution_id !== 'string' || !input.execution_id) {
    return sendError(res, 400, 'invalid_request');
  }
  const result = runtime.cancel(input.execution_id);
  return sendJSON(res, result.status === 'accepted' ? 202 : 200, result);
}

async function upload(req, res, runtime) {
  const part = parseMultipart(await readBody(req), req.headers['content-type']);
  return sendJSON(res, 200, runtime.files.upload(part.filename, part.data));
}

async function fileGET(req, res, runtime, operation, relative) {
  if (operation === 'exists') {
    return sendJSON(res, 200, {exists: runtime.files.exists(relative)});
  }
  if (operation === 'list') {
    const result = runtime.files.list(relative);
    return result ? sendJSON(res, 200, result) : sendError(res, 404, 'not_found');
  }
  const result = runtime.files.download(relative);
  if (!result) return sendError(res, 404, 'not_found');
  const stream = result.stream;
  stream.on('error', () => res.destroy());
  res.writeHead(200, {
    'content-type': 'application/octet-stream',
    'content-length': result.size,
  });
  return stream.pipe(res);
}

export function createRouter(runtime) {
  const routes = [
    ['GET', '/', (req, res) => health(req, res, runtime)],
    ['GET', '/manifest', (req, res) => manifest(req, res, runtime)],
    ['POST', '/execute', (req, res) => execute(req, res, runtime)],
    ['POST', '/cancel', (req, res) => cancel(req, res, runtime)],
    ['POST', '/upload', (req, res) => upload(req, res, runtime)],
  ];
  return async function route(req, res) {
    try {
      const url = new URL(req.url, 'http://runtime');
      for (const [method, pathname, handler] of routes) {
        if (req.method === method && url.pathname === pathname) {
          return await handler(req, res);
        }
      }
      const match = /^(download|exists|list)(?:\/(.*))?$/.exec(url.pathname.slice(1));
      if (req.method === 'GET' && match) {
        return await fileGET(req, res, runtime, match[1], match[2] || '');
      }
      return sendError(res, 404, 'not_found');
    } catch (error) {
      const status = error.status || 400;
      return sendError(res, status, status === 413 ? 'payload_too_large' : 'invalid_request');
    }
  };
}

export function startServer(runtime, port) {
  const route = createRouter(runtime);
  return http
    .createServer((req, res) => {
      route(req, res).catch(() => {
        if (!res.headersSent) sendError(res, 400, 'invalid_request');
      });
    })
    .listen(port, '0.0.0.0');
}
