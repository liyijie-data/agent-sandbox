import fs from 'node:fs';
import path from 'node:path';

const MAX_READ = 256 * 1024;
const MAX_WRITE = 1024 * 1024;
const DEFINITIONS = [
  {
    type: 'function',
    function: {
      name: 'agent_list_files',
      description: 'List workspace or output files.',
      parameters: {
        type: 'object',
        properties: {path: {type: 'string'}},
        additionalProperties: false,
      },
    },
  },
  {
    type: 'function',
    function: {
      name: 'agent_read_file',
      description: 'Read a UTF-8 text file.',
      parameters: {
        type: 'object',
        properties: {path: {type: 'string'}},
        required: ['path'],
        additionalProperties: false,
      },
    },
  },
  {
    type: 'function',
    function: {
      name: 'agent_write_file',
      description: 'Write a UTF-8 text file.',
      parameters: {
        type: 'object',
        properties: {path: {type: 'string'}, content: {type: 'string'}},
        required: ['path', 'content'],
        additionalProperties: false,
      },
    },
  },
];
function result(value) {
  return {ok: true, result: value};
}
function failure(error) {
  return {
    ok: false,
    error_type: 'ToolError',
    error: [
      'unsafe path',
      'protocol path',
      'directory not found',
      'file is too large or binary',
      'file is too large',
      'invalid arguments',
    ].includes(error.message)
      ? error.message
      : 'tool failed',
  };
}
function target(root, value, write = false) {
  if (typeof value !== 'string' || !value || value.includes('\\') || path.isAbsolute(value))
    throw new Error('unsafe path');
  const clean = value.replace(/^\//, '');
  const base = clean.startsWith('output/') || clean === 'output' ? 'output' : 'workspace';
  const rel =
    clean === base ? '' : clean.startsWith(`${base}/`) ? clean.slice(base.length + 1) : clean;
  if (!rel || rel.split('/').some((part) => !part || part === '.' || part === '..'))
    throw new Error('unsafe path');
  if (
    (!write && rel.startsWith('.skill-archives')) ||
    (write &&
      ((base === 'output' && (rel === 'result.json' || rel === 'checkpoint.tar.gz')) ||
        rel.startsWith('.skill-') || rel === '.skills' || rel.startsWith('.skills/')))
  )
    throw new Error('protocol path');
  const baseDir = path.join(root, base);
  if (fs.lstatSync(baseDir, {throwIfNoEntry: false})?.isSymbolicLink())
    throw new Error('unsafe path');
  const targetPath = path.resolve(baseDir, rel);
  if (!targetPath.startsWith(`${baseDir}${path.sep}`)) throw new Error('unsafe path');
  let current = baseDir;
  for (const part of rel.split('/')) {
    current = path.join(current, part);
    if (fs.lstatSync(current, {throwIfNoEntry: false})?.isSymbolicLink())
      throw new Error('unsafe path');
  }
  return targetPath;
}
function list(root, value) {
  const base =
    value === 'output' || value === '/output'
      ? path.join(root, 'output')
      : value === 'workspace' || value === '/workspace' || !value
        ? path.join(root, 'workspace')
        : target(root, value);
  if (fs.lstatSync(base, {throwIfNoEntry: false})?.isSymbolicLink()) throw new Error('unsafe path');
  if (!fs.existsSync(base) || !fs.statSync(base).isDirectory())
    throw new Error('directory not found');
  return result({
    entries: fs
      .readdirSync(base)
      .filter((entry) => !entry.startsWith('.skill'))
      .sort()
      .slice(0, 500),
  });
}

export function builtinDefinitions() {
  return DEFINITIONS;
}
export function executeBuiltin(root, name, argsJSON) {
  try {
    const args = JSON.parse(argsJSON || '{}');
    if (!args || typeof args !== 'object' || Array.isArray(args))
      throw new Error('invalid arguments');
    const allowed = name === 'agent_write_file' ? new Set(['path', 'content']) : new Set(['path']);
    if (Object.keys(args).some((key) => !allowed.has(key))) throw new Error('invalid arguments');
    if (name === 'agent_list_files') return list(root, args.path);
    if (name === 'agent_read_file') {
      const file = target(root, args.path);
      const stat = fs.lstatSync(file);
      if (!stat.isFile() || stat.size > MAX_READ) throw new Error('file is too large or binary');
      let fd;
      try {
        fd = fs.openSync(file, fs.constants.O_RDONLY | fs.constants.O_NOFOLLOW);
        const data = Buffer.alloc(stat.size);
        let offset = 0;
        while (offset < data.length) {
          const count = fs.readSync(fd, data, offset, data.length - offset, offset);
          if (!count) throw new Error('file changed');
          offset += count;
        }
        if (data.includes(0)) throw new Error('file is too large or binary');
        return result({path: args.path, content: data.toString('utf8')});
      } finally {
        if (fd !== undefined) fs.closeSync(fd);
      }
    }
    if (name === 'agent_write_file') {
      if (typeof args.content !== 'string' || Buffer.byteLength(args.content, 'utf8') > MAX_WRITE)
        throw new Error('file is too large');
      const file = target(root, args.path, true);
      fs.mkdirSync(path.dirname(file), {recursive: true});
      fs.writeFileSync(file, args.content);
      return result({path: args.path, size_bytes: Buffer.byteLength(args.content, 'utf8')});
    }
    return {ok: false, error_type: 'ToolUnauthorized', error: 'tool is not registered'};
  } catch (error) {
    return failure(error);
  }
}
