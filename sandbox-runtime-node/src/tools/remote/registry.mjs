import {createOpenAPI} from './openapi.mjs';
import {createMCP} from './mcp.mjs';
import {invalid} from './errors.mjs';
import {createNameAllocator} from './names.mjs';
import {builtinDefinitions} from '../builtin.mjs';

export async function createRemoteTools({config = [], signal, deadline} = {}) {
  if (!Array.isArray(config)) throw invalid('tools must be an array');
  const reserved = [...builtinDefinitions().map((item) => item.function.name), 'agent_request_input'];
  const nameAllocator = createNameAllocator(reserved);
  const adapters = []; const definitions = []; const owners = new Map();
  try {
    for (const tool of config) {
      const adapter = tool.type === 'openapi' ? await createOpenAPI(tool, {signal, deadline, nameAllocator}) : tool.type === 'mcp' ? await createMCP(tool, {signal, deadline, nameAllocator}) : (() => { throw invalid('unsupported remote tool type'); })();
      adapters.push(adapter);
      for (const definition of adapter.definitions) { if (owners.has(definition.function.name)) throw invalid('duplicate tool name'); owners.set(definition.function.name, adapter); definitions.push(definition); }
    }
  } catch (error) { await Promise.allSettled(adapters.map((item) => item.close())); throw error; }
  return {definitions, execute: async (name, argsJSON) => owners.has(name) ? owners.get(name).execute(name, argsJSON) : {ok: false, error_type: 'ToolUnauthorized', error: 'tool is not registered'}, close: async () => { await Promise.allSettled(adapters.map((item) => item.close())); }};
}

export {createOpenAPI, createMCP};
