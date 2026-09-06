import fs from 'node:fs';
import path from 'node:path';
import {buildZip, sha256} from './zip.mjs';

const MAX_BYTES = 64 * 1024 * 1024;
const MAX_FILES = 4096;
const SKIP_OUTPUT = new Set(['result.json', 'checkpoint.tar.gz']);

function artifactError(code) {
  return Object.assign(new Error(code), {code});
}

function safeRelative(name) {
  if (
    !name ||
    name.includes('\0') ||
    name.includes('\\') ||
    name.length > 1024 ||
    path.posix.isAbsolute(name)
  )
    throw artifactError('result_bundle_upload_failed');
  const parts = name.split('/');
  if (parts.some((part) => !part || part === '.' || part === '..'))
    throw artifactError('result_bundle_upload_failed');
  return name;
}

function readFileBounded(file, remaining) {
  let fd;
  try {
    fd = fs.openSync(file, fs.constants.O_RDONLY | fs.constants.O_NOFOLLOW);
    const before = fs.fstatSync(fd);
    if (!before.isFile()) throw artifactError('result_bundle_upload_failed');
    if (before.size > remaining || before.size > MAX_BYTES)
      throw artifactError('resource_limit_exceeded');
    const data = Buffer.alloc(before.size);
    let offset = 0;
    while (offset < data.length) {
      const count = fs.readSync(fd, data, offset, data.length - offset, offset);
      if (!count) throw artifactError('result_bundle_upload_failed');
      offset += count;
    }
    const extra = Buffer.alloc(1);
    if (fs.readSync(fd, extra, 0, 1, before.size) !== 0)
      throw artifactError('result_bundle_upload_failed');
    const after = fs.fstatSync(fd);
    if (after.size !== before.size || data.length !== before.size)
      throw artifactError('result_bundle_upload_failed');
    return data;
  } catch (error) {
    if (error.code === 'resource_limit_exceeded' || error.code === 'result_bundle_upload_failed')
      throw error;
    throw artifactError('result_bundle_upload_failed');
  } finally {
    if (fd !== undefined) fs.closeSync(fd);
  }
}

function walk(root, visit) {
  let rootReal;
  try {
    const rootStat = fs.lstatSync(root);
    if (rootStat.isSymbolicLink() || !rootStat.isDirectory())
      throw artifactError('result_bundle_upload_failed');
    rootReal = fs.realpathSync(root);
  } catch (error) {
    if (error.code === 'ENOENT') return;
    if (error.code === 'result_bundle_upload_failed') throw error;
    throw artifactError('result_bundle_upload_failed');
  }
  const visitDir = (relative) => {
    const current = relative ? path.join(rootReal, relative) : rootReal;
    for (const entry of fs
      .readdirSync(current, {withFileTypes: true})
      .sort((a, b) => a.name.localeCompare(b.name))) {
      const rel = safeRelative(relative ? `${relative}/${entry.name}` : entry.name);
      const full = path.join(rootReal, rel);
      if (entry.isSymbolicLink()) continue;
      if (entry.isDirectory()) visitDir(rel);
      else if (entry.isFile()) visit(rel, full);
    }
  };
  try {
    visitDir('');
  } catch (error) {
    if (error.code === 'resource_limit_exceeded' || error.code === 'result_bundle_upload_failed')
      throw error;
    throw artifactError('result_bundle_upload_failed');
  }
}

export function workspaceBaseline(root) {
  const result = {};
  let total = 0;
  let count = 0;
  walk(root, (rel, file) => {
    count += 1;
    if (count > MAX_FILES) throw artifactError('resource_limit_exceeded');
    const data = readFileBounded(file, MAX_BYTES);
    total += data.length;
    if (total > MAX_BYTES) throw artifactError('resource_limit_exceeded');
    result[rel] = sha256(data);
  });
  return {workspace: result};
}

export function collectArtifacts(root, baseline) {
  const entries = [];
  let total = 0;
  const add = (source, directory) =>
    walk(directory, (rel, file) => {
      if (source === 'output' && (SKIP_OUTPUT.has(rel) || /^result\.json\.tmp-/.test(rel))) return;
      if (rel.startsWith('.skill')) return;
      if (entries.length >= MAX_FILES) throw artifactError('resource_limit_exceeded');
      const data = readFileBounded(file, source === 'workspace' ? MAX_BYTES : MAX_BYTES - total);
      const digest = sha256(data);
      if (source === 'workspace' && baseline?.workspace?.[rel] === digest) return;
      total += data.length;
      if (total > MAX_BYTES) throw artifactError('resource_limit_exceeded');
      entries.push({source, name: rel, data, sha256: digest, size_bytes: data.length});
    });
  add('output', path.join(root, 'output'));
  add('workspace', path.join(root, 'workspace'));
  entries.sort((a, b) => a.source.localeCompare(b.source) || a.name.localeCompare(b.name));
  return entries;
}

export function makeBundle(entries) {
  const manifest = {
    artifacts: entries.map(({source, name, sha256: digest, size_bytes}) => ({
      source,
      name,
      sha256: digest,
      size_bytes,
    })),
  };
  const zipEntries = [{name: 'manifest.json', data: Buffer.from(JSON.stringify(manifest), 'utf8')}];
  for (const entry of entries)
    zipEntries.push({
      name: `${entry.source === 'output' ? 'artifacts' : 'workspace'}/${entry.name}`,
      data: entry.data,
    });
  try {
    const zip = buildZip(zipEntries);
    if (zip.length > MAX_BYTES) throw artifactError('resource_limit_exceeded');
    return {zip, sha256: sha256(zip), size_bytes: zip.length};
  } catch (error) {
    if (error.code === 'resource_limit_exceeded') throw error;
    throw artifactError('result_bundle_upload_failed');
  }
}
