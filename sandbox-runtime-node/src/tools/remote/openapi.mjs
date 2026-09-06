import crypto from 'node:crypto';
import {request, jsonBody} from './http.mjs';
import {accessHeaders} from './access.mjs';
import {LIMITS, deadlineMs} from './limits.mjs';
import {invalid, transport} from './errors.mjs';
import {createNameAllocator} from './names.mjs';
import {deref, expandSchema, validateSchema} from './schema.mjs';

const METHODS = new Set(['get', 'post', 'put', 'patch', 'delete', 'head', 'options']);

function rejectRemoteRefs(value, seen = new Set()) {
  if (!value || typeof value !== 'object') return;
  if (seen.has(value)) return;
  seen.add(value);
  if ('$ref' in value && (typeof value.$ref !== 'string' || !value.$ref.startsWith('#/'))) throw invalid('remote schema references are forbidden');
  for (const child of Object.values(value)) rejectRemoteRefs(child, seen);
}

async function loadSpec(config, signal, deadline) {
  const ref = config.spec;
  if (!ref || typeof ref.download_url !== 'string' || typeof ref.sha256 !== 'string') throw invalid('openapi spec resource is incomplete');
  const response = await request(ref.download_url, {signal, timeoutMs: Math.min(deadlineMs(deadline), LIMITS.timeoutMs), maxResponseBytes: Math.min(ref.size_bytes || LIMITS.specBytes, LIMITS.specBytes)});
  if (response.status < 200 || response.status >= 300) throw transport('spec download failed', response.status);
  const raw = Buffer.from(response.body);
  if (ref.size_bytes !== undefined && raw.length !== Number(ref.size_bytes)) throw invalid('spec size mismatch');
  if (crypto.createHash('sha256').update(raw).digest('hex') !== ref.sha256) throw invalid('spec hash mismatch');
  let spec;
  try { spec = JSON.parse(raw.toString('utf8')); } catch { throw invalid('spec is not valid JSON'); }
  if (!spec || typeof spec !== 'object' || !/^(3\.0|3\.1)(?:\.|$)/.test(String(spec.openapi || ''))) throw invalid('only OpenAPI 3.x is supported');
  rejectRemoteRefs(spec);
  if (!spec.paths || typeof spec.paths !== 'object') throw invalid('spec has no paths');
  return spec;
}

function operation(spec, path, method, op) {
  const params = [...(spec.paths[path].parameters || []), ...(op.parameters || [])].map((item) => deref(spec, item));
  const template = [...path.matchAll(/\{([^}]+)\}/g)].map((m) => m[1]);
  const pathParams = params.filter((p) => p?.in === 'path').map((p) => p.name);
  if (template.sort().join() !== pathParams.sort().join()) throw invalid('path parameters do not match template');
  for (const p of params) if (!p?.name || !['path', 'query'].includes(p.in)) throw invalid('unsupported OpenAPI parameter');
  let body = null;
  if (op.requestBody) {
    const rb = deref(spec, op.requestBody); const content = rb?.content;
    if (!content?.['application/json']) throw invalid('only application/json request bodies are supported');
    body = {required: rb.required === true, schema: content['application/json'].schema || {type: 'object'}};
  }
  return {path, method, params, body, summary: op.summary || op.description || `${method.toUpperCase()} ${path}`};
}

export async function createOpenAPI(config, {signal, deadline, nameAllocator} = {}) {
  if (!config?.id || typeof config.base_url !== 'string' || !Array.isArray(config.allowed_operations) || !config.allowed_operations.length) throw invalid('invalid OpenAPI tool configuration');
  const base = new URL(config.base_url); if (!['http:', 'https:'].includes(base.protocol) || base.username || base.password) throw invalid('invalid OpenAPI base URL');
  const spec = await loadSpec(config, signal, deadline);
  const operations = new Map();
  for (const [path, item] of Object.entries(spec.paths)) {
    if (!path.startsWith('/') || path.includes('://') || !item || typeof item !== 'object') throw invalid('invalid OpenAPI path');
    for (const [method, op] of Object.entries(item)) if (METHODS.has(method)) {
      if (!op?.operationId || operations.has(op.operationId)) throw invalid('operationId is missing or duplicated');
      operations.set(op.operationId, operation(spec, path, method, op));
    }
  }
  if (operations.size > LIMITS.maxOperations || config.allowed_operations.some((id) => !operations.has(id))) throw invalid('OpenAPI allowlist does not match spec');
  const access = accessHeaders(config.access);
  const allocator = nameAllocator || createNameAllocator(); const names = new Map();
  const definitions = config.allowed_operations.map((id) => {
    const op = operations.get(id); const properties = {}; const required = [];
    for (const p of op.params) { properties[p.name] = expandSchema(spec, p.schema || {type: 'string'}); if (p.required) required.push(p.name); }
    if (op.body) { properties.body = expandSchema(spec, op.body.schema); if (op.body.required) required.push('body'); }
    const finalName = allocator.allocate(id); names.set(finalName, id);
    const definition = {type: 'function', function: {name: finalName, description: op.summary, parameters: {type: 'object', properties, required, additionalProperties: false}}};
    if (Buffer.byteLength(JSON.stringify(definition)) > LIMITS.schemaBytes) throw invalid('tool schema exceeds limit');
    return definition;
  });
  async function execute(name, argsJSON) {
    const id = names.get(name);
    if (!id) return {ok: false, error_type: 'ToolUnauthorized', error: 'operation is not allowed'};
    let args; try { args = JSON.parse(argsJSON || '{}'); } catch { return {ok: false, error_type: 'ToolArgumentsInvalid', error: 'arguments are not valid JSON'}; }
    const op = operations.get(id); try { validateSchema(spec, {type: 'object', properties: Object.fromEntries(op.params.map((p) => [p.name, p.schema || {type: 'string'}]).concat(op.body ? [['body', op.body.schema]] : [])), required: [...op.params.filter((p) => p.required).map((p) => p.name), ...(op.body?.required ? ['body'] : [])], additionalProperties: false}, args); } catch (error) { return {ok: false, error_type: 'ToolArgumentsInvalid', error: error.message}; }
    let path = op.path;
    for (const p of op.params.filter((item) => item.in === 'path')) path = path.replace(`{${p.name}}`, encodeURIComponent(String(args[p.name])));
    const url = new URL(base.toString());
    const prefix = base.pathname.replace(/\/$/, '');
    const resolvedPath = `${prefix}/${path.replace(/^\//, '')}`;
    if (resolvedPath.includes('..') || decodeURIComponent(resolvedPath).split('/').includes('..')) throw invalid('path escapes base URL');
    url.pathname = resolvedPath;
    const query = new URLSearchParams();
    for (const p of op.params.filter((item) => item.in === 'query')) if (args[p.name] !== undefined) query.set(p.name, String(args[p.name]));
    url.search = query.toString();
    const headers = {'accept': 'application/json', ...access}; const body = op.body && args.body !== undefined ? jsonBody(args.body) : undefined;
    if (body !== undefined) headers['content-type'] = 'application/json';
    try { const response = await request(url, {method: op.method.toUpperCase(), headers, body, signal, timeoutMs: Math.min(deadlineMs(deadline), LIMITS.timeoutMs)}); if (response.status < 200 || response.status >= 300) return {ok: false, error_type: 'OpenApiHttpError', error: `HTTP ${response.status}`, status: response.status, retryable: response.status >= 500 || response.status === 429}; let data; try { data = JSON.parse(response.body); } catch { data = response.body; } return {ok: true, status: response.status, ...(typeof data === 'string' ? {text: data} : {data})}; } catch (error) { return {ok: false, error_type: error.type || 'OpenApiTransportError', error: error.message}; }
  }
  return {definitions, execute, close: async () => {}};
}
