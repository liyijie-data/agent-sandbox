import crypto from 'node:crypto';
import fs from 'node:fs/promises';
import path from 'node:path';
import zlib from 'node:zlib';
import {parseTar} from '../recovery/tar.mjs';

const MAX_ARCHIVE = 64 * 1024 * 1024;
const MAX_TOTAL = 512 * 1024 * 1024;
const MAX_MEMBER = 64 * 1024 * 1024;
const MAX_ENTRIES = 4096;
const MAX_SKILL_MD = 1024 * 1024;
function error(code) {
  return Object.assign(new Error(code), {code});
}
function safeName(name) {
  if (
    !name ||
    name.length > 1024 ||
    name.includes('\\') ||
    name.includes('\0') ||
    name.startsWith('/') ||
    name.split('/').some((part) => !part || part === '.' || part === '..')
  )
    throw error('skill_archive_unsafe');
  return name;
}
function crc32(data) {
  let crc = 0xffffffff;
  for (const byte of data) {
    crc ^= byte;
    for (let i = 0; i < 8; i++) crc = (crc >>> 1) ^ (0xedb88320 & -(crc & 1));
  }
  return (crc ^ 0xffffffff) >>> 0;
}
function u16(data, offset) {
  return data.readUInt16LE(offset);
}
function u32(data, offset) {
  return data.readUInt32LE(offset);
}

function zipEntries(data) {
  const entries = [];
  let offset = 0;
  const seen = new Set();
  let total = 0;
  while (offset + 4 <= data.length && data.readUInt32LE(offset) === 0x04034b50) {
    if (entries.length >= MAX_ENTRIES) throw error('skill_limit_exceeded');
    if (offset + 30 > data.length) throw error('skill_archive_unsafe');
    const flags = u16(data, offset + 6);
    const method = u16(data, offset + 8);
    const compressed = u32(data, offset + 18);
    const size = u32(data, offset + 22);
    const nameLength = u16(data, offset + 26);
    const extraLength = u16(data, offset + 28);
    if (
      flags & 0x09 ||
      (method !== 0 && method !== 8) ||
      size > MAX_MEMBER ||
      compressed > MAX_MEMBER
    )
      throw error('skill_archive_unsafe');
    const rawName = data.subarray(offset + 30, offset + 30 + nameLength).toString('utf8');
    const directory = rawName.endsWith('/');
    const name = safeName(directory ? rawName.slice(0, -1) : rawName);
    if (seen.has(name)) throw error('skill_archive_unsafe');
    seen.add(name);
    const start = offset + 30 + nameLength + extraLength;
    const end = start + compressed;
    if (end > data.length) throw error('skill_archive_unsafe');
    const raw = data.subarray(start, end);
    const content =
      method === 0 ? Buffer.from(raw) : zlib.inflateRawSync(raw, {maxOutputLength: MAX_MEMBER});
    if (content.length !== size || crc32(content) !== u32(data, offset + 14))
      throw error('skill_archive_unsafe');
    total += content.length;
    if (total > MAX_TOTAL) throw error('skill_limit_exceeded');
    entries.push({name, data: content, directory});
    offset = end;
  }
  if (
    !entries.length ||
    offset + 4 > data.length ||
    (data.readUInt32LE(offset) !== 0x02014b50 && data.readUInt32LE(offset) !== 0x06054b50)
  )
    throw error('skill_archive_unsafe');
  let central = offset;
  for (let index = 0; index < entries.length; index++) {
    if (central + 46 > data.length || data.readUInt32LE(central) !== 0x02014b50)
      throw error('skill_archive_unsafe');
    const mode = u32(data, central + 38) >>> 16;
    if ((mode & 0o170000) === 0o120000) throw error('skill_archive_unsafe');
    central += 46 + u16(data, central + 28) + u16(data, central + 30) + u16(data, central + 32);
  }
  return entries;
}

async function writeEntries(entries, staging) {
  for (const entry of entries) {
    if (!entry.name) continue;
    const target = path.join(staging, entry.name);
    if (entry.directory) {
      await fs.mkdir(target, {recursive: true});
      continue;
    }
    await fs.mkdir(path.dirname(target), {recursive: true});
    await fs.writeFile(target, entry.data, {flag: 'wx'});
  }
}

export async function extractSkill(buffer, staging) {
  if (buffer.length > MAX_ARCHIVE) throw error('skill_limit_exceeded');
  let entries;
  try {
    if (buffer.subarray(0, 4).equals(Buffer.from('PK\x03\x04', 'binary')))
      entries = zipEntries(buffer);
    else if (buffer.subarray(0, 2).equals(Buffer.from([0x1f, 0x8b]))) {
      const members = parseTar(zlib.gunzipSync(buffer, {maxOutputLength: MAX_TOTAL}));
      entries = [...members].map(([name, member]) => ({
        name,
        data: member.data,
        directory: member.directory,
      }));
      if (entries.length > MAX_ENTRIES) throw error('skill_limit_exceeded');
    } else throw error('skill_unsupported_format');
  } catch (caught) {
    if (caught.code) throw caught;
    throw error('skill_archive_unsafe');
  }
  await writeEntries(entries, staging);
  const direct = path.join(staging, 'SKILL.md');
  const top = (await fs.readdir(staging, {withFileTypes: true})).filter(
    (entry) => entry.name !== '.DS_Store',
  );
  let root = direct;
  if (
    !(await fs
      .stat(direct)
      .then((stat) => stat.isFile())
      .catch(() => false))
  ) {
    if (
      top.length !== 1 ||
      !top[0].isDirectory() ||
      !(await fs
        .stat(path.join(staging, top[0].name, 'SKILL.md'))
        .then((stat) => stat.isFile())
        .catch(() => false))
    )
      throw error('skill_missing_manifest');
    root = path.join(staging, top[0].name);
  } else root = staging;
  const md = await fs.readFile(path.join(root, 'SKILL.md'));
  if (md.length > MAX_SKILL_MD) throw error('skill_limit_exceeded');
  return {root, instructions: md.toString('utf8')};
}

export {MAX_ARCHIVE};
