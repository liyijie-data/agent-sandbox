import OpenAI from 'openai';
import {OpenAIChatCompletionsModel} from '@openai/agents';

const MAX_OUTPUT_BYTES = 2 * 1024 * 1024;

class ModelStreamError extends Error {
  constructor(code) { super(code); this.code = code; }
}

function mapReasoningInMessages(messages) {
  return (messages || []).map((message) => {
    if (!message || typeof message !== 'object' || !Object.hasOwn(message, 'reasoning')) return message;
    const {reasoning, ...rest} = message;
    return {...rest, reasoning_content: reasoning};
  });
}

function outputSize(delta) {
  if (!delta || typeof delta !== 'object') return 0;
  let size = 0;
  for (const value of [delta.content, delta.reasoning, delta.reasoning_content])
    if (typeof value === 'string') size += Buffer.byteLength(value, 'utf8');
  for (const call of delta.tool_calls || []) {
    if (typeof call?.function?.name === 'string') size += Buffer.byteLength(call.function.name, 'utf8');
    if (typeof call?.function?.arguments === 'string') size += Buffer.byteLength(call.function.arguments, 'utf8');
  }
  return size;
}

function instrumentStream(response, state) {
  if (!response.body) return response;
  let buffer = '';
  const decoder = new TextDecoder();
  const transform = new TransformStream({
    transform(chunk, controller) {
      buffer += decoder.decode(chunk, {stream: true});
      const lines = buffer.split('\n'); buffer = lines.pop() || '';
      for (const rawLine of lines) markSseLine(rawLine, state);
      if (Buffer.byteLength(buffer, 'utf8') > MAX_OUTPUT_BYTES) throw new ModelStreamError('resource_limit_exceeded');
      controller.enqueue(chunk);
    },
    flush() {
      buffer += decoder.decode();
      if (buffer) markSseLine(buffer, state);
      if (!state.doneFrame) throw new ModelStreamError('agent_execution_failed');
    },
  });
  return new Response(response.body.pipeThrough(transform), {
    status: response.status,
    statusText: response.statusText,
    headers: response.headers,
  });
}

function markSseLine(rawLine, state) {
  const line = rawLine.endsWith('\r') ? rawLine.slice(0, -1) : rawLine;
  if (line.startsWith('data:') && line.slice(5).trim() === '[DONE]') state.doneFrame = true;
}

function wrapStream(stream, state, onReasoning) {
  return {
    controller: stream.controller,
    async *[Symbol.asyncIterator]() {
      const iterator = stream[Symbol.asyncIterator]();
      try {
        for await (const chunk of { [Symbol.asyncIterator]: () => iterator }) {
          const choice = chunk?.choices?.[0];
          if (choice?.finish_reason) state.finished = true;
          if (chunk?.error || choice?.error) throw new ModelStreamError('agent_execution_failed');
          const delta = choice?.delta;
          state.bytes += outputSize(delta);
          if (state.bytes > MAX_OUTPUT_BYTES) throw new ModelStreamError('resource_limit_exceeded');
          if (delta && Object.hasOwn(delta, 'reasoning_content')) {
            if (typeof onReasoning === 'function' && typeof delta.reasoning_content === 'string') await onReasoning(delta.reasoning_content);
            delta.reasoning = delta.reasoning_content;
            delete delta.reasoning_content;
          } else if (typeof onReasoning === 'function' && typeof delta?.reasoning === 'string') {
            await onReasoning(delta.reasoning);
          }
          yield chunk;
        }
        if (!state.doneFrame || !state.finished) throw new ModelStreamError('agent_execution_failed');
      } finally {
        await iterator.return?.();
      }
    },
  };
}

function createFetch(deadline, cancel, onReasoning, stateRef) {
  return async (input, init = {}) => {
    let nextInit = init;
    if (typeof init.body === 'string') {
      try {
        const body = JSON.parse(init.body);
        if (Array.isArray(body.messages)) nextInit = {...init, body: JSON.stringify({...body, messages: mapReasoningInMessages(body.messages)})};
      } catch {}
    }
    const signal = AbortSignal.any([cancel.signal, init.signal, AbortSignal.timeout(Math.max(1, deadline.remaining()) * 1000)].filter(Boolean));
    const response = await fetch(input, {...nextInit, signal});
    let stream = false;
    try { stream = JSON.parse(nextInit.body).stream === true; } catch {}
    if (!stream || !response.ok) return response;
    const state = stateRef.current || {bytes: 0, finished: false, doneFrame: false};
    stateRef.current = state;
    return instrumentStream(response, state);
  };
}

export function createGatewayModel(cfg, deadline, cancel, {onReasoning} = {}) {
  const stateRef = {current: null};
  const client = new OpenAI({apiKey: cfg.model.token, baseURL: cfg.model.base_url, maxRetries: 0, fetch: createFetch(deadline, cancel, onReasoning, stateRef)});
  const create = client.chat.completions.create.bind(client.chat.completions);
  client.chat.completions.create = (body, options) => {
    const original = create(body, options);
    if (!body.stream) return original;
    const state = {bytes: 0, finished: false, doneFrame: false};
    stateRef.current = state;
    const withResponse = original.withResponse?.bind(original);
    if (withResponse) original.withResponse = async () => {
      const result = await withResponse();
      return {...result, data: wrapStream(result.data, state, onReasoning)};
    };
    return original;
  };
  const model = new OpenAIChatCompletionsModel(client, cfg.model.name);
  const getStreamedResponse = model.getStreamedResponse.bind(model);
  model.getStreamedResponse = function(request) {
    const stream = getStreamedResponse(request);
    return (async function*() {
      for await (const event of stream) {
        if (event.type === 'response_done' && event.response && Array.isArray(event.response.output)) {
          const hasAssistantOutput = event.response.output.some((item) =>
            item?.type === 'message' || item?.type === 'function_call');
          if (!hasAssistantOutput) {
            event.response.output.push({
              type: 'message',
              role: 'assistant',
              status: 'completed',
              content: [{type: 'output_text', text: ''}],
            });
          }
        }
        yield event;
      }
    })();
  };
  return model;
}
