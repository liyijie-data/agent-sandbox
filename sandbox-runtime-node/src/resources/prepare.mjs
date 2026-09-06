import crypto from 'node:crypto';
import fs from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import {downloadVerified} from './download.mjs';
import {extractSkill} from './archive.mjs';

function error(code) {
  return Object.assign(new Error(code), {code});
}
function safePath(name) {
  if (
    typeof name !== 'string' ||
    !name ||
    name.length > 1024 ||
    name.includes('\\') ||
    name.includes('\0') ||
    name.startsWith('/') ||
    name.split('/').some((part) => !part || part === '.' || part === '..')
  )
    throw error('resource_unsafe_path');
  const first = name.split('/')[0];
  if (
    [
      'run.json',
      'result.json',
      'input',
      'output',
      'workspace',
      '.skills',
      '.skill-archives',
    ].includes(first) ||
    first.startsWith('.skill-')
  )
    throw error('resource_unsafe_path');
  return name;
}
function skillName(name) {
  if (
    typeof name !== 'string' ||
    !name ||
    name.length > 128 ||
    /[\\/\0]/.test(name) ||
    name === '.' ||
    name === '..'
  )
    throw error('resource_unsafe_path');
  return name;
}
async function writeAtomic(root, name, data) {
  const workspace = path.join(root, 'workspace');
  const workspaceStat = await fs.lstat(workspace).catch(() => null);
  if (!workspaceStat?.isDirectory() || workspaceStat.isSymbolicLink())
    throw error('resource_unsafe_path');
  await ensureParents(workspace, name);
  const target = path.join(workspace, name);
  const temp = `${target}.part-${process.pid}`;
  try {
    await fs.writeFile(temp, data, {flag: 'wx'});
    await fs.rename(temp, target);
  } catch (caught) {
    await fs.rm(temp, {force: true});
    if (caught.code) throw caught;
    throw error('resource_download_failed');
  }
}
async function ensureParents(base, relative) {
  let current = base;
  for (const part of relative.split('/').slice(0, -1)) {
    current = path.join(current, part);
    const stat = await fs.lstat(current).catch(() => null);
    if (stat?.isSymbolicLink() || (stat && !stat.isDirectory()))
      throw error('resource_unsafe_path');
    if (!stat) await fs.mkdir(current);
  }
}
async function hashFile(file) {
  const stat = await fs.lstat(file);
  if (!stat.isFile() || stat.size > 256 * 1024 * 1024) throw error('skill_rebuild_failed');
  const data = await fs.readFile(file);
  return {sha256: crypto.createHash('sha256').update(data).digest('hex'), size_bytes: data.length};
}

async function prepareFiles(root, files, options) {
  const seen = new Set();
  for (const resource of files || []) {
    const name = safePath(resource?.name);
    if (seen.has(name)) throw error('resource_duplicate_path');
    seen.add(name);
    const data = await downloadVerified(resource, {...options, maxBytes: 64 * 1024 * 1024});
    await writeAtomic(root, name, data);
  }
}
async function prepareSkills(root, skills, options) {
  const seen = new Set();
  const workspace = path.join(root, 'workspace');
  for (const resource of skills || []) {
    const name = skillName(resource?.name);
    if (seen.has(name)) throw error('resource_duplicate_path');
    seen.add(name);
    const data = await downloadVerified(resource, options);
    const temp = await fs.mkdtemp(path.join(os.tmpdir(), 'node-skill-'));
    try {
      const extracted = await extractSkill(data, temp);
      const final = path.join(workspace, '.skills', name);
      const archiveDir = path.join(workspace, '.skill-archives');
      await ensureParents(workspace, `.skills/${name}/x`);
      await fs.rm(final, {recursive: true, force: true});
      await fs.cp(extracted.root, final, {recursive: true, errorOnExist: true});
      await ensureParents(workspace, `.skill-archives/${name}.zip`);
      const ext = data[0] === 0x1f ? '.tar.gz' : '.zip';
      await fs.writeFile(path.join(archiveDir, `${name}${ext}`), data, {flag: 'wx'});
    } finally {
      await fs.rm(temp, {recursive: true, force: true});
    }
  }
}
async function rebuildSkills(root, skills, {signal, deadline} = {}) {
  const workspace = path.join(root, 'workspace');
  const seen = new Set();
  for (const resource of skills || []) {
    if (signal?.aborted) throw error('skill_rebuild_failed');
    if (deadline?.expired()) throw error('skill_rebuild_failed');
    const name = skillName(resource?.name);
    if (seen.has(name)) throw error('resource_duplicate_path');
    seen.add(name);
    const archiveDir = path.join(workspace, '.skill-archives');
    let found;
    for (const ext of ['.zip', '.tar.gz']) {
      const candidate = path.join(archiveDir, `${name}${ext}`);
      try {
        const meta = await hashFile(candidate);
        if (meta.sha256 === resource.sha256 && meta.size_bytes === resource.size_bytes) {
          found = candidate;
          break;
        }
      } catch {}
    }
    if (!found) throw error('skill_rebuild_failed');
    const temp = await fs.mkdtemp(path.join(os.tmpdir(), 'node-skill-rebuild-'));
    try {
      const extracted = await extractSkill(await fs.readFile(found), temp);
      const final = path.join(workspace, '.skills', name);
      await ensureParents(workspace, `.skills/${name}/x`);
      await fs.rm(final, {recursive: true, force: true});
      await fs.cp(extracted.root, final, {recursive: true});
    } finally {
      await fs.rm(temp, {recursive: true, force: true});
    }
  }
}

export async function prepareResources({root, config, signal, deadline, resume = false}) {
  const options = {signal, deadline};
  if (resume) await rebuildSkills(root, config.skills, {signal, deadline});
  else {
    await prepareFiles(root, config.files, options);
    await prepareSkills(root, config.skills, options);
  }
  const lines = [];
  let instructionBytes = 0;
  for (const file of config.files || []) lines.push(`已提供文件：workspace/${file.name}`);
  for (const skill of config.skills || []) {
    const md = await fs.readFile(
      path.join(root, 'workspace', '.skills', skill.name, 'SKILL.md'),
      'utf8',
    );
    const instruction = `已提供 skill：workspace/.skills/${skill.name}\n${md.slice(0, 1024 * 1024)}`;
    instructionBytes += Buffer.byteLength(instruction);
    if (instructionBytes > 1024 * 1024) throw error('skill_limit_exceeded');
    lines.push(instruction);
  }
  return {instructions: lines.join('\n\n')};
}
