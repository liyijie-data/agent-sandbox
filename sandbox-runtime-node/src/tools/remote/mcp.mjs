import {MCPServerStreamableHttp, mcpToFunctionTool} from '@openai/agents';
import {accessHeaders} from './access.mjs';
import {LIMITS, deadlineMs} from './limits.mjs';
import {invalid} from './errors.mjs';
import {createNameAllocator} from './names.mjs';
import {expandSchema, validateSchema} from './schema.mjs';

function resultValue(result) {
  if (!result || typeof result !== 'object' || !Array.isArray(result.content)) throw new Error('invalid MCP tool result');
  const text = []; const json = [];
  for (const item of result.content) {
    if (item?.type === 'text' && typeof item.text === 'string') text.push(item.text);
    else if (item?.type === 'json') json.push(item.json);
    else if (item?.type === 'resource' || item?.type === 'resource_link') throw new Error('MCP remote resources are forbidden');
    else throw new Error('unsupported MCP content');
  }
  return {ok: !result.isError, ...(result.isError ? {error_type: 'McpToolError', error: text.join('\n') || 'MCP tool failed'} : {result: {text: text.join('\n'), json, ...(result.structuredContent === undefined ? {} : {structured: result.structuredContent})}})};
}

export async function createMCP(config, {signal, deadline, nameAllocator} = {}) {
  if (!config?.id || config.transport !== 'streamable_http' || typeof config.url !== 'string' || !Array.isArray(config.allowed_operations) || !config.allowed_operations.length) throw invalid('invalid MCP tool configuration');
  const endpoint = new URL(config.url);
  if (!['http:', 'https:'].includes(endpoint.protocol) || endpoint.username || endpoint.password) throw invalid('invalid MCP URL');
  const access = accessHeaders(config.access);
  const fetchWithLimits = async (input, init = {}) => {
    if (signal?.aborted || init.signal?.aborted) throw (signal?.reason || init.signal?.reason || new DOMException('aborted', 'AbortError'));
    const timeoutSignal = AbortSignal.timeout(Math.min(deadlineMs(deadline), LIMITS.timeoutMs));
    const combinedSignal = AbortSignal.any([timeoutSignal, signal, init.signal].filter(Boolean));
    const response = await fetch(input, {...init, redirect: 'error', signal: combinedSignal});
      if (response.redirected) throw new Error('MCP redirects are forbidden');
      if (!response.body) return response;
      let total = 0;
      const body = response.body.pipeThrough(new TransformStream({transform(chunk, controller) { total += chunk.byteLength; if (total > LIMITS.responseBytes) { controller.error(new Error('MCP response exceeds limit')); return; } controller.enqueue(chunk); }}));
      return new Response(body, {status: response.status, statusText: response.statusText, headers: response.headers});
  };
  if (signal?.aborted) throw signal.reason || new DOMException('aborted', 'AbortError');
  const server = new MCPServerStreamableHttp({url: endpoint.toString(), name: config.id, cacheToolsList: true, clientSessionTimeoutSeconds: Math.max(1, Math.min(30, Math.ceil(deadlineMs(deadline) / 1000))), requestInit: {headers: access}, fetch: fetchWithLimits, errorFunction: null});
  const names = new Map(); const tools = new Map();
  try {
    await server.connect();
    const discovered = await server.listTools(); const byName = new Map();
    for (const item of discovered) {
      if (!item || typeof item.name !== 'string' || byName.has(item.name)) throw new Error('MCP tools/list contains invalid or duplicate tool');
      const schema = item.inputSchema || {type: 'object'};
      byName.set(item.name, {...item, inputSchema: expandSchema(schema, schema)});
    }
    if (byName.size > LIMITS.maxTools || config.allowed_operations.some((name) => !byName.has(name))) throw new Error('MCP allowlist does not match server tools');
    const allocator = nameAllocator || createNameAllocator();
    const definitions = config.allowed_operations.map((remoteName) => {
      const item = byName.get(remoteName); const finalName = allocator.allocate(remoteName); names.set(finalName, remoteName);
      const sdkTool = mcpToFunctionTool(item, server, false, {toolNameOverride: finalName, errorFunction: null}); tools.set(finalName, {item, sdkTool});
      const definition = {type: 'function', function: {name: sdkTool.name, description: sdkTool.description, parameters: sdkTool.parameters}};
      if (Buffer.byteLength(JSON.stringify(definition)) > LIMITS.schemaBytes) throw invalid('tool schema exceeds limit');
      return definition;
    });
    return {
      definitions,
      execute: async (name, argsJSON) => {
        const entry = tools.get(name); if (!entry || !names.has(name)) return {ok: false, error_type: 'ToolUnauthorized', error: 'tool is not allowed'};
        let args; try { args = JSON.parse(argsJSON || '{}'); } catch { return {ok: false, error_type: 'ToolArgumentsInvalid', error: 'arguments are not valid JSON'}; }
        if (!args || typeof args !== 'object' || Array.isArray(args)) return {ok: false, error_type: 'ToolArgumentsInvalid', error: 'arguments must be an object'};
        try {
          validateSchema(entry.item.inputSchema || {type: 'object'}, entry.item.inputSchema || {type: 'object'}, args);
          return resultValue(await server.callToolResult(entry.item.name, args, undefined, {signal}));
        } catch (error) {
          const argumentError = error.type === 'ToolConfigError';
          return {ok: false, error_type: argumentError ? 'ToolArgumentsInvalid' : error.type || 'McpTransportError', error: error.message, retryable: !argumentError};
        }
      },
      close: async () => { try { await server.close(); } catch {} },
    };
  } catch (error) { try { await server.close(); } catch {} throw error; }
}
