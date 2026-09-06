import {failure} from '../protocol/errors.mjs';

function diagnose(phase, error) {
  const code = error?.code || 'unknown';
  const name = error?.name || 'Error';
  const status = Number.isInteger(error?.status) ? ` status=${error.status}` : '';
  process.stderr.write(`runtime: ${phase} failed code=${code} name=${name}${status}\n`);
}

async function retryWait(ms, deadline, cancel) {
  if (cancel.signal.aborted || deadline.expired()) throw failure('steering_delivery_failed');
  await new Promise((resolve, reject) => {
    const timer = setTimeout(resolve, Math.min(ms, Math.max(1, deadline.remaining() * 1000)));
    const stop = () => {
      clearTimeout(timer);
      reject(failure('steering_delivery_failed'));
    };
    cancel.signal.addEventListener('abort', stop, {once: true});
    setTimeout(
      () => cancel.signal.removeEventListener('abort', stop),
      Math.min(ms, Math.max(1, deadline.remaining() * 1000)),
    );
  });
}

async function postRuntime(cfg, endpoint, request, deadline, cancel, timeoutSeconds = 10) {
  try {
    const timeout = Math.min(timeoutSeconds * 1000, Math.max(1, deadline.remaining()) * 1000);
    const signal = AbortSignal.any([cancel.signal, AbortSignal.timeout(timeout)]);
    const response = await fetch(`${cfg.runtime.base_url.replace(/\/$/, '')}${endpoint}`, {
      method: 'POST',
      headers: {'content-type': 'application/json', authorization: `Bearer ${cfg.runtime.token}`},
      body: JSON.stringify(request),
      signal,
    });
    if (!response.ok)
      throw Object.assign(failure('steering_delivery_failed'), {status: response.status});
    return await response.json();
  } catch (error) {
    diagnose(endpoint, error);
    throw failure('steering_delivery_failed');
  }
}

async function retryRuntime(cfg, endpoint, request, deadline, cancel) {
  let last;
  for (let attempt = 0; attempt < 3; attempt++) {
    try {
      return await postRuntime(cfg, endpoint, request, deadline, cancel);
    } catch (error) {
      last = error;
      if (attempt < 2) await retryWait(1000 * 2 ** attempt, deadline, cancel);
    }
  }
  throw last || failure('steering_delivery_failed');
}

function validateBatch(batch, cursor) {
  if (
    !batch ||
    (batch.items !== null && batch.items !== undefined && !Array.isArray(batch.items)) ||
    !Number.isInteger(batch.through_seq) ||
    batch.through_seq < cursor
  )
    throw failure('steering_delivery_failed');
  const items = batch.items || [];
  if (!items.length) {
    if ((batch.batch_id !== null && batch.batch_id !== undefined) || batch.through_seq !== cursor)
      throw failure('steering_delivery_failed');
    return;
  }
  let expected = cursor + 1;
  for (const item of items) {
    if (
      !Number.isInteger(item.seq) ||
      item.seq !== expected ||
      typeof item.steer_id !== 'string' ||
      !item.message ||
      item.message.role !== 'user' ||
      typeof item.message.content !== 'string'
    )
      throw failure('steering_delivery_failed');
    expected++;
  }
  if (batch.batch_id === null || batch.batch_id === undefined || batch.through_seq !== expected - 1)
    throw failure('steering_delivery_failed');
}

export async function consumeSteering(cfg, messages, cursor, deadline, cancel) {
  const batch = await retryRuntime(
    cfg,
    '/steers/pull',
    {execution_id: cfg.execution_id, after_seq: cursor},
    deadline,
    cancel,
  );
  validateBatch(batch, cursor);
  if (!(batch.items || []).length) return cursor;
  for (const item of batch.items) messages.push(item.message);
  const ack = await retryRuntime(
    cfg,
    '/steers/ack',
    {
      execution_id: cfg.execution_id,
      batch_id: batch.batch_id,
      incorporated_through_seq: batch.through_seq,
    },
    deadline,
    cancel,
  );
  if (!ack || ack.incorporated_through_seq !== batch.through_seq)
    throw failure('steering_delivery_failed');
  return batch.through_seq;
}

export async function emitEvent(cfg, type, text, deadline, sequence, cancel) {
  const maxBytes = 64 * 1024;
  let part = '';
  let size = 0;
  const send = async (value) => {
    if (!value) return;
    try {
      await postRuntime(
        cfg,
        '/events',
        {
          execution_id: cfg.execution_id,
          source_seq: ++sequence.value,
          type,
          payload: {delta: value},
        },
        deadline,
        cancel,
        2,
      );
    } catch {
    }
  };
  for (const char of text) {
    const bytes = Buffer.byteLength(char, 'utf8');
    if (part && size + bytes > maxBytes) {
      await send(part);
      part = '';
      size = 0;
    }
    part += char;
    size += bytes;
  }
  await send(part);
}

export async function emitToolEvent(
  cfg,
  type,
  toolCallId,
  toolName,
  status,
  deadline,
  sequence,
  cancel,
  result = undefined,
) {
  try {
    await postRuntime(
      cfg,
      '/events',
      {
        execution_id: cfg.execution_id,
        source_seq: ++sequence.value,
        type,
        payload: {
          tool_call_id: toolCallId,
          tool_name: toolName,
          status,
          ...(result === undefined ? {} : {result}),
        },
      },
      deadline,
      cancel,
      2,
    );
  } catch {
  }
}
