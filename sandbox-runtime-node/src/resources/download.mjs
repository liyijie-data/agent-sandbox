import fs from 'node:fs/promises';
import crypto from 'node:crypto';

const MAX_BYTES = 256 * 1024 * 1024;

function error(code) {
  return Object.assign(new Error(code), {code});
}
function checkedUrl(value) {
  let url;
  try {
    url = new URL(value);
  } catch {
    throw error('resource_invalid_entry');
  }
  if (!['http:', 'https:'].includes(url.protocol) || url.username || url.password)
    throw error('resource_invalid_entry');
  return url;
}

export async function downloadVerified(resource, {signal, deadline, maxBytes = MAX_BYTES} = {}) {
  if (
    !resource ||
    typeof resource !== 'object' ||
    typeof resource.download_url !== 'string' ||
    typeof resource.sha256 !== 'string'
  )
    throw error('resource_invalid_entry');
  checkedUrl(resource.download_url);
  if (
    !Number.isSafeInteger(resource.size_bytes) ||
    resource.size_bytes < 0 ||
    resource.size_bytes > maxBytes
  )
    throw error('resource_invalid_entry');
  if (!/^[a-f0-9]{64}$/.test(resource.sha256)) throw error('resource_invalid_entry');
  const timeout = Math.min(30_000, Math.max(1, deadline ? deadline.remaining() * 1000 : 30_000));
  const timer = AbortSignal.timeout(timeout);
  const combined = signal ? AbortSignal.any([signal, timer]) : timer;
  let response;
  try {
    response = await fetch(resource.download_url, {redirect: 'error', signal: combined});
    if (!response.ok || !response.body) throw error('resource_download_failed');
    const reader = response.body.getReader();
    const chunks = [];
    let total = 0;
    const hash = crypto.createHash('sha256');
    while (true) {
      const {done, value} = await reader.read();
      if (done) break;
      total += value.byteLength;
      if (total > maxBytes) throw error('resource_download_too_large');
      if (total > resource.size_bytes) throw error('resource_size_mismatch');
      hash.update(value);
      chunks.push(Buffer.from(value));
    }
    if (total !== resource.size_bytes) throw error('resource_size_mismatch');
    if (hash.digest('hex') !== resource.sha256) throw error('resource_hash_mismatch');
    return Buffer.concat(chunks, total);
  } catch (caught) {
    if (signal?.aborted) throw error('resource_download_failed');
    if (deadline?.expired()) throw error('resource_download_failed');
    if (caught.code) throw caught;
    throw error('resource_download_failed');
  } finally {
    await response?.body?.cancel().catch(() => {});
  }
}

export {checkedUrl};
