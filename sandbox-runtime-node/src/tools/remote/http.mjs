import {LIMITS} from './limits.mjs';
import {transport, invalid} from './errors.mjs';

export async function request(url, options = {}) {
  let parsed;
  try { parsed = new URL(url); } catch { throw invalid('invalid remote URL'); }
  if (!['http:', 'https:'].includes(parsed.protocol) || parsed.username || parsed.password) throw invalid('invalid remote URL');
  const body = options.body;
  const bytes = body === undefined ? 0 : Buffer.byteLength(typeof body === 'string' ? body : JSON.stringify(body));
  const maxRequest = options.maxRequestBytes || LIMITS.requestBytes;
  if (bytes > maxRequest) throw transport('request exceeds size limit');
  const timeout = Math.min(options.timeoutMs || LIMITS.timeoutMs, LIMITS.timeoutMs);
  const timer = AbortSignal.timeout(Math.max(1, timeout));
  const signal = options.signal ? AbortSignal.any([options.signal, timer]) : timer;
  let response;
  try {
    response = await fetch(parsed, {method: options.method || 'GET', headers: options.headers, body, redirect: 'manual', signal});
  } catch (error) {
    if (options.signal?.aborted) throw transport('remote tool request cancelled');
    if (error?.name === 'TimeoutError' || error?.name === 'AbortError') throw transport('remote tool request timed out');
    throw transport('remote tool request failed');
  }
  if (response.status >= 300 && response.status < 400) {
    await response.body?.cancel();
    throw transport('redirect rejected', response.status);
  }
  const reader = response.body?.getReader();
  const chunks = []; let total = 0; const maxResponse = options.maxResponseBytes || LIMITS.responseBytes;
  if (reader) {
    while (true) {
      let part;
      try { part = await reader.read(); } catch { throw transport('remote tool response failed'); }
      if (part.done) break;
      total += part.value.byteLength;
      if (total > maxResponse) { await reader.cancel(); throw transport('response exceeds size limit'); }
      chunks.push(part.value);
      if (options.onChunk?.(Buffer.concat(chunks).toString('utf8')) === true) {
        await reader.cancel();
        break;
      }
    }
  }
  const data = Buffer.concat(chunks).toString('utf8');
  return {status: response.status, headers: Object.fromEntries(response.headers), body: data};
}

export function jsonBody(value) {
  try { return JSON.stringify(value); } catch { throw invalid('arguments are not serializable'); }
}
