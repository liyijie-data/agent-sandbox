import {collectArtifacts, makeBundle} from './collector.mjs';

function fail(code) {
  throw Object.assign(new Error(code), {code});
}

function validateDestination(destination) {
  if (!destination || typeof destination !== 'object' || Array.isArray(destination))
    fail('runtime_protocol_invalid');
  const allowed = new Set(['destination_id', 'upload_url', 'signature_query_keys', 'expires_at']);
  if (Object.keys(destination).some((key) => !allowed.has(key))) fail('runtime_protocol_invalid');
  if (
    typeof destination.destination_id !== 'string' ||
    !destination.destination_id ||
    typeof destination.upload_url !== 'string'
  )
    fail('runtime_protocol_invalid');
  let url;
  try {
    url = new URL(destination.upload_url);
  } catch {
    fail('runtime_protocol_invalid');
  }
  if (!['http:', 'https:'].includes(url.protocol) || url.username || url.password)
    fail('runtime_protocol_invalid');
  if (
    typeof destination.expires_at !== 'string' ||
    !Number.isFinite(Date.parse(destination.expires_at))
  )
    fail('runtime_protocol_invalid');
  return destination;
}

function uploadSignal(signal, deadline) {
  const timeout = AbortSignal.timeout(
    Math.min(60_000, Math.max(1, deadline ? deadline.remaining() * 1000 : 60_000)),
  );
  return signal ? AbortSignal.any([signal, timeout]) : timeout;
}

export async function deliverArtifacts({root, destination, baseline, signal, deadline}) {
  const entries = collectArtifacts(root, baseline);
  if (!entries.length)
    return destination
      ? {status: 'empty', destination_id: destination.destination_id}
      : {status: 'not_requested'};
  if (!destination) fail('artifact_destination_required');
  const checked = validateDestination(destination);
  if (Date.parse(checked.expires_at) <= Date.now()) fail('result_bundle_upload_failed');
  const bundle = makeBundle(entries);
  if (signal?.aborted) fail('run_cancelled');
  if (deadline?.expired()) fail('execution_timeout');
  try {
    const response = await fetch(checked.upload_url, {
      method: 'PUT',
      headers: {'content-type': 'application/zip'},
      body: bundle.zip,
      redirect: 'error',
      signal: uploadSignal(signal, deadline),
    });
    await response.body?.cancel();
    if (!response.ok) fail('result_bundle_upload_failed');
  } catch (error) {
    if (signal?.aborted) fail('run_cancelled');
    if (deadline?.expired()) fail('execution_timeout');
    if (error.code === 'result_bundle_upload_failed') throw error;
    fail('result_bundle_upload_failed');
  }
  return {
    status: 'uploaded',
    destination_id: checked.destination_id,
    sha256: bundle.sha256,
    size_bytes: bundle.size_bytes,
  };
}
