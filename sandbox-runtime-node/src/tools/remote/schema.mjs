import {invalid} from './errors.mjs';

export function deref(root, schema, depth = 0, seen = new Set()) {
  if (!schema || typeof schema !== 'object' || Array.isArray(schema)) return schema;
  if (!schema.$ref) return schema;
  if (depth >= 16 || typeof schema.$ref !== 'string' || !schema.$ref.startsWith('#/')) throw invalid('unsupported schema reference');
  if (seen.has(schema.$ref)) throw invalid('schema reference cycle');
  const target = schema.$ref.slice(2).split('/').map((part) => part.replaceAll('~1', '/').replaceAll('~0', '~')).reduce((node, key) => node && Object.prototype.hasOwnProperty.call(node, key) ? node[key] : undefined, root);
  if (!target) throw invalid('schema reference not found');
  return deref(root, target, depth + 1, new Set([...seen, schema.$ref]));
}

const SUPPORTED = new Set(['$ref', 'type', 'title', 'description', 'default', 'enum', 'const', 'properties', 'required', 'additionalProperties', 'items', 'oneOf', 'anyOf', 'allOf', 'minLength', 'maxLength', 'pattern', 'minimum', 'maximum', 'exclusiveMinimum', 'exclusiveMaximum', 'minItems', 'maxItems', 'uniqueItems', 'format']);

export function expandSchema(root, schema, depth = 0, state = {nodes: 0, refs: new Set()}) {
  if (!schema || typeof schema !== 'object' || Array.isArray(schema)) throw invalid('schema must be an object');
  if (++state.nodes > 256 || depth > 16) throw invalid('schema exceeds complexity limit');
  const resolved = schema.$ref ? deref(root, schema, depth, state.refs) : schema;
  if (resolved !== schema) return expandSchema(root, resolved, depth + 1, state);
  for (const key of Object.keys(schema)) if (!SUPPORTED.has(key)) throw invalid(`unsupported schema keyword ${key}`);
  if (schema.type !== undefined && !['object', 'array', 'string', 'integer', 'number', 'boolean', 'null'].includes(schema.type)) throw invalid('unsupported schema type');
  if (schema.pattern !== undefined) throw invalid('pattern constraints are not supported');
  if (schema.format !== undefined) throw invalid('format constraints are not supported');
  const out = {...schema};
  if (schema.properties !== undefined) {
    if (!schema.properties || typeof schema.properties !== 'object' || Array.isArray(schema.properties)) throw invalid('schema properties must be an object');
    out.properties = Object.fromEntries(Object.entries(schema.properties).map(([key, value]) => [key, expandSchema(root, value, depth + 1, state)]));
  }
  if (schema.items) out.items = expandSchema(root, schema.items, depth + 1, state);
  for (const key of ['oneOf', 'anyOf', 'allOf']) if (schema[key]) {
    if (!Array.isArray(schema[key])) throw invalid(`${key} must be an array`);
    out[key] = schema[key].map((value) => expandSchema(root, value, depth + 1, state));
  }
  return out;
}

export function validateSchema(root, schema, value, path = 'arguments') {
  schema = expandSchema(root, schema || {});
  if (schema.oneOf || schema.anyOf) {
    const alternatives = schema.oneOf || schema.anyOf;
    const matches = alternatives.filter((candidate) => { try { validateSchema(root, candidate, value, path); return true; } catch { return false; } }).length;
    if (matches < 1 || (schema.oneOf && matches !== 1)) throw invalid(`${path} violates schema`);
  }
  if (schema.allOf) for (const candidate of schema.allOf) validateSchema(root, candidate, value, path);
  if (schema.const !== undefined && JSON.stringify(value) !== JSON.stringify(schema.const)) throw invalid(`${path} violates schema`);
  if (schema.enum && (!Array.isArray(schema.enum) || !schema.enum.some((item) => JSON.stringify(item) === JSON.stringify(value)))) throw invalid(`${path} violates schema`);
  if (schema.type === 'object') {
    if (!value || typeof value !== 'object' || Array.isArray(value)) throw invalid(`${path} must be an object`);
    const props = schema.properties || {};
    for (const key of schema.required || []) if (!Object.prototype.hasOwnProperty.call(value, key)) throw invalid(`${path}.${key} is required`);
    for (const key of Object.keys(value)) if (!Object.prototype.hasOwnProperty.call(props, key)) {
      if (schema.additionalProperties === false) throw invalid(`${path}.${key} is unknown`);
      if (schema.additionalProperties && typeof schema.additionalProperties === 'object') validateSchema(root, schema.additionalProperties, value[key], `${path}.${key}`);
    }
    for (const [key, child] of Object.entries(props)) if (Object.prototype.hasOwnProperty.call(value, key)) validateSchema(root, child, value[key], `${path}.${key}`);
  } else if (schema.type === 'array') {
    if (!Array.isArray(value)) throw invalid(`${path} must be an array`);
    if (schema.minItems !== undefined && value.length < schema.minItems) throw invalid(`${path} has too few items`);
    if (schema.maxItems !== undefined && value.length > schema.maxItems) throw invalid(`${path} has too many items`);
    if (schema.uniqueItems && new Set(value.map((item) => JSON.stringify(item))).size !== value.length) throw invalid(`${path} items must be unique`);
    if (schema.items) value.forEach((item, index) => validateSchema(root, schema.items, item, `${path}[${index}]`));
  } else if (schema.type === 'null') {
    if (value !== null) throw invalid(`${path} must be null`);
  } else if (schema.type === 'string' || schema.type === undefined && typeof value === 'string') {
    if (typeof value !== 'string') throw invalid(`${path} must be a string`);
    if (schema.minLength !== undefined && value.length < schema.minLength || schema.maxLength !== undefined && value.length > schema.maxLength) throw invalid(`${path} length violates schema`);
  } else if ((schema.type === 'integer' || schema.type === 'number') || schema.type === undefined && typeof value === 'number') {
    if (schema.type === 'integer' && !Number.isInteger(value) || typeof value !== 'number' || !Number.isFinite(value)) throw invalid(`${path} must be a number`);
    if (schema.minimum !== undefined && value < schema.minimum || schema.exclusiveMinimum !== undefined && value <= schema.exclusiveMinimum || schema.maximum !== undefined && value > schema.maximum || schema.exclusiveMaximum !== undefined && value >= schema.exclusiveMaximum) throw invalid(`${path} number violates schema`);
  } else if (schema.type === 'integer' && (!Number.isInteger(value))) throw invalid(`${path} must be an integer`);
  else if (schema.type === 'number' && (typeof value !== 'number' || !Number.isFinite(value))) throw invalid(`${path} must be a number`);
  else if (schema.type === 'boolean' && typeof value !== 'boolean') throw invalid(`${path} must be a boolean`);
}
