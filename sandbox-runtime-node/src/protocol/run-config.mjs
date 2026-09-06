import fs from 'node:fs';

function invalid() {
  return Object.assign(new Error('runtime_protocol_invalid'), {code: 'runtime_protocol_invalid'});
}

function validateMessages(messages) {
  if (!Array.isArray(messages) || !messages.length) throw invalid();
  const declared = new Set();
  const answered = new Set();
  for (const message of messages) {
    if (
      !message ||
      !['system', 'developer', 'user', 'assistant', 'tool'].includes(message.role) ||
      (message.content !== undefined &&
        message.content !== null &&
        typeof message.content !== 'string')
    ) {
      throw invalid();
    }
    for (const call of message.tool_calls || []) {
      if (
        message.role !== 'assistant' ||
        !call?.id ||
        call.type !== 'function' ||
        !call.function?.name ||
        declared.has(call.id)
      ) {
        throw invalid();
      }
      declared.add(call.id);
    }
    if (message.role === 'tool') {
      if (
        typeof message.tool_call_id !== 'string' ||
        !declared.has(message.tool_call_id) ||
        answered.has(message.tool_call_id)
      ) {
        throw invalid();
      }
      answered.add(message.tool_call_id);
    }
  }
  for (const id of declared) {
    if (!answered.has(id)) throw invalid();
  }
}

function validateResultBundle(bundle) {
  if (bundle === undefined || bundle === null) return;
  if (!bundle || typeof bundle !== 'object' || Array.isArray(bundle)) throw invalid();
  const allowed = new Set(['destination_id', 'upload_url', 'signature_query_keys', 'expires_at']);
  if (Object.keys(bundle).some((key) => !allowed.has(key))) throw invalid();
  if (
    typeof bundle.destination_id !== 'string' ||
    !bundle.destination_id ||
    typeof bundle.upload_url !== 'string' ||
    !bundle.upload_url
  )
    throw invalid();
  let upload;
  try {
    upload = new URL(bundle.upload_url);
  } catch {
    throw invalid();
  }
  if (!['http:', 'https:'].includes(upload.protocol) || upload.username || upload.password)
    throw invalid();
  if (
    bundle.signature_query_keys !== undefined &&
    (!Array.isArray(bundle.signature_query_keys) ||
      bundle.signature_query_keys.some((key) => typeof key !== 'string' || !key))
  )
    throw invalid();
  if (
    typeof bundle.expires_at !== 'string' ||
    !Number.isFinite(Date.parse(bundle.expires_at)) ||
    Date.parse(bundle.expires_at) <= Date.now()
  )
    throw invalid();
}

export function loadRunConfig(root) {
  try {
    const cfg = JSON.parse(fs.readFileSync(`${root}/run.json`, 'utf8'));
    if (
      !cfg ||
      typeof cfg !== 'object' ||
      Array.isArray(cfg) ||
      cfg.contract_version !== 'agent-platform-runtime/v1'
    ) {
      throw invalid();
    }
    for (const key of ['run_id', 'execution_id']) {
      if (typeof cfg[key] !== 'string' || !cfg[key]) throw invalid();
    }
    if (!Number.isInteger(cfg.stage) || cfg.stage < 1 || !Number.isInteger(cfg.fence))
      throw invalid();
    if (
      !cfg.model ||
      typeof cfg.model !== 'object' ||
      !cfg.model.name ||
      !cfg.model.base_url ||
      !cfg.model.token
    ) {
      throw invalid();
    }
    if (
      !cfg.runtime ||
      typeof cfg.runtime !== 'object' ||
      !cfg.runtime.base_url ||
      !cfg.runtime.token
    ) {
      throw invalid();
    }
    if (
      !cfg.limits ||
      !Number.isInteger(cfg.limits.remaining_execution_seconds) ||
      cfg.limits.remaining_execution_seconds <= 0
    ) {
      throw invalid();
    }
    if (cfg.messages !== undefined) validateMessages(cfg.messages);
    validateResultBundle(cfg.result_bundle);
    const allowed = new Set([
      'contract_version',
      'run_id',
      'stage',
      'fence',
      'execution_id',
      'messages',
      'model',
      'runtime',
      'limits',
      'resume',
      'steering',
      'files',
      'skills',
      'tools',
      'result_bundle',
    ]);
    if (Object.keys(cfg).some((key) => !allowed.has(key))) throw invalid();
    if (
      cfg.steering !== undefined &&
      (!Number.isInteger(cfg.steering.after_seq) || cfg.steering.after_seq < 0)
    ) {
      throw invalid();
    }
    return cfg;
  } catch (error) {
    throw Object.assign(new Error(error.message), {code: error.code || 'runtime_protocol_invalid'});
  }
}

export {invalid as protocolError};
