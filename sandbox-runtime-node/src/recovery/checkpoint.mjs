import fs from 'node:fs';
import path from 'node:path';
import crypto from 'node:crypto';
import zlib from 'node:zlib';
import {MAX_PACKAGE_BYTES, makeTar, parseTar, safeName} from './tar.mjs';
const STATE_NAME = 'agent/state.bin';
const hash = (data) => crypto.createHash('sha256').update(data).digest('hex');

function jsonBytes(value) {
  return Buffer.from(JSON.stringify(value), 'utf8');
}

function collectFiles(root, prefix, entries, files, budget) {
  if (!fs.existsSync(root)) return;
  const walk = (current, relative) => {
    for (const entry of fs
      .readdirSync(current, {withFileTypes: true})
      .sort((a, b) => a.name.localeCompare(b.name))) {
      const full = path.join(current, entry.name);
      const rel = relative ? `${relative}/${entry.name}` : entry.name;
      const name = safeName(`${prefix}/${rel}`);
      if (name === 'output/checkpoint.tar.gz' || name === 'output/result.json') continue;
      if (entry.isSymbolicLink()) continue;
      if (entry.isDirectory()) {
        walk(full, rel);
        continue;
      }
      if (!entry.isFile()) throw new Error('unsupported workspace file');
      const size = fs.statSync(full).size;
      budget.used += size;
      if (budget.used > budget.max) throw new Error('package exceeds allowed size');
      const data = fs.readFileSync(full);
      files.set(name, data);
      entries[name] = {sha256: hash(data), size_bytes: data.length};
    }
  };
  walk(root, '');
}

export function buildCheckpoint({workspace, output, state, baseline, cursor, cfg, dest}) {
  const maxBytes = Math.min(
    MAX_PACKAGE_BYTES,
    Number(cfg.limits?.checkpoint_max_bytes || MAX_PACKAGE_BYTES),
  );
  if (!Number.isSafeInteger(maxBytes) || maxBytes <= 0)
    throw new Error('invalid checkpoint size limit');
  const budget = {used: 0, max: maxBytes};
  const entries = {};
  const files = new Map([
    [STATE_NAME, Buffer.isBuffer(state) ? state : jsonBytes({messages: state, cursor})],
    ['baseline.json', jsonBytes(baseline)],
    ['steering.json', jsonBytes({incorporated_through_seq: cursor})],
  ]);
  for (const [name, data] of files) {
    entries[name] = {sha256: hash(data), size_bytes: data.length};
  }
  budget.used += [...files.values()].reduce((sum, data) => sum + data.length, 0);
  if (budget.used > maxBytes) throw new Error('package exceeds allowed size');
  collectFiles(workspace, 'workspace', entries, files, budget);
  collectFiles(output, 'output', entries, files, budget);
  const total = Object.values(entries).reduce((sum, item) => sum + item.size_bytes, 0);
  const manifest = {
    contract_version: cfg.contract_version,
    image_digest: '',
    state_format: 'node-runtime/1',
    run_id: cfg.run_id,
    stage: cfg.stage,
    fence: cfg.fence || 0,
    entries,
    total_size_bytes: total,
  };
  const archiveFiles = new Map([['manifest.json', jsonBytes(manifest)], ...files]);
  const archive = zlib.gzipSync(makeTar(archiveFiles, ['agent', 'workspace', 'output']), {
    mtime: 0,
  });
  if (archive.length > maxBytes || total + archiveFiles.get('manifest.json').length > maxBytes) {
    throw new Error('package exceeds allowed size');
  }
  fs.mkdirSync(path.dirname(dest), {recursive: true});
  fs.writeFileSync(dest, archive);
  return {
    path: dest,
    format: 'node-runtime/1',
    sha256: hash(archive),
    size_bytes: archive.length,
  };
}

export function restoreCheckpoint(tgz, root, options = {}) {
  const maxBytes = Math.min(MAX_PACKAGE_BYTES, Number(options.maxBytes || MAX_PACKAGE_BYTES));
  if (!Number.isSafeInteger(maxBytes) || maxBytes <= 0)
    throw new Error('invalid checkpoint size limit');
  const compressed = fs.readFileSync(tgz);
  if (compressed.length > maxBytes) throw new Error('package exceeds allowed size');
  const members = parseTar(zlib.gunzipSync(compressed, {maxOutputLength: maxBytes}));
  for (const name of ['manifest.json', STATE_NAME, 'baseline.json', 'steering.json']) {
    if (!members.has(name) || members.get(name).directory)
      throw new Error(`package missing ${name}`);
  }
  const manifest = JSON.parse(members.get('manifest.json').data.toString('utf8'));
  if (
    manifest.contract_version !== 'agent-platform-runtime/v1' ||
    manifest.state_format !== 'node-runtime/1' ||
    typeof manifest.run_id !== 'string' ||
    !manifest.run_id ||
    !Number.isInteger(manifest.stage) ||
    !Number.isInteger(manifest.fence) ||
    !manifest.entries ||
    typeof manifest.entries !== 'object' ||
    Array.isArray(manifest.entries)
  ) {
    throw new Error('invalid checkpoint manifest');
  }
  if (options.runId !== undefined && manifest.run_id !== options.runId)
    throw new Error('checkpoint run identity mismatch');
  if (options.beforeStage !== undefined && manifest.stage >= options.beforeStage)
    throw new Error('checkpoint stage is not resumable');
  const entries = manifest.entries;
  let total = 0;
  for (const [name, member] of members) {
    if (member.directory) {
      if (
        name !== 'agent' &&
        name !== 'workspace' &&
        name !== 'output' &&
        !name.startsWith('workspace/') &&
        !name.startsWith('output/')
      ) {
        throw new Error(`unknown checkpoint directory ${name}`);
      }
      continue;
    }
    if (name === 'manifest.json') continue;
    const declared = entries[name];
    if (
      !declared ||
      declared.size_bytes !== member.data.length ||
      declared.sha256 !== hash(member.data)
    ) {
      throw new Error(`member ${name} failed hash/size verification`);
    }
    total += member.data.length;
  }
  if (manifest.total_size_bytes !== total || total > maxBytes)
    throw new Error('checkpoint size metadata mismatch');
  for (const name of Object.keys(entries)) {
    if (!members.has(name) || members.get(name).directory)
      throw new Error(`manifest entry missing ${name}`);
  }
  const steering = JSON.parse(members.get('steering.json').data.toString('utf8'));
  if (!Number.isInteger(steering.incorporated_through_seq) || steering.incorporated_through_seq < 0)
    throw new Error('invalid steering metadata');
  const state = JSON.parse(members.get(STATE_NAME).data.toString('utf8'));
  if (!Number.isInteger(state.cursor) || state.cursor !== steering.incorporated_through_seq)
    throw new Error('state and steering cursors differ');
  const baseline = JSON.parse(members.get('baseline.json').data.toString('utf8'));
  const workspace = path.join(root, 'workspace');
  const output = path.join(root, 'output');
  fs.rmSync(workspace, {recursive: true, force: true});
  fs.rmSync(output, {recursive: true, force: true});
  for (const [name, member] of members) {
    if (member.directory || (!name.startsWith('workspace/') && !name.startsWith('output/')))
      continue;
    const targetRoot = name.startsWith('workspace/') ? workspace : output;
    const relative = name.slice(name.indexOf('/') + 1);
    const target = path.join(targetRoot, relative);
    fs.mkdirSync(path.dirname(target), {recursive: true});
    fs.writeFileSync(target, member.data, {flag: 'wx'});
  }
  return {state, baseline, cursor: steering.incorporated_through_seq};
}
