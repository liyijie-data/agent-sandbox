export const MAX_PACKAGE_BYTES = 512 * 1024 * 1024;
export const MAX_MEMBERS = 10000;

export function safeName(name) {
  const normalized = name.replaceAll('\\', '/').replace(/\/$/, '');
  if (
    !normalized ||
    normalized.startsWith('/') ||
    normalized.split('/').some((part) => !part || part === '.' || part === '..')
  ) {
    throw new Error('unsafe checkpoint path');
  }
  return normalized;
}

function octal(value, width) {
  const text = value.toString(8);
  return Buffer.from(text.padStart(width - 1, '0') + '\0');
}

function tarHeader(name, size, type) {
  const nameBytes = Buffer.from(name);
  if (nameBytes.length > 100) throw new Error('checkpoint path exceeds tar name limit');
  const header = Buffer.alloc(512, 0);
  nameBytes.copy(header, 0);
  octal(0o644, 8).copy(header, 100);
  octal(0, 8).copy(header, 108);
  octal(0, 8).copy(header, 116);
  octal(size, 12).copy(header, 124);
  octal(Math.floor(Date.now() / 1000), 12).copy(header, 136);
  header.fill(0x20, 148, 156);
  header[156] = type.charCodeAt(0);
  Buffer.from('ustar\0').copy(header, 257);
  Buffer.from('00').copy(header, 263);
  const checksum = header.reduce((total, byte) => total + byte, 0);
  octal(checksum, 8).copy(header, 148);
  return header;
}

export function makeTar(files, directories) {
  const chunks = [];
  for (const directory of directories) chunks.push(tarHeader(`${directory}/`, 0, '5'));
  for (const [name, data] of files) {
    chunks.push(tarHeader(name, data.length, '0'));
    chunks.push(data);
    const padding = (512 - (data.length % 512)) % 512;
    if (padding) chunks.push(Buffer.alloc(padding));
  }
  chunks.push(Buffer.alloc(1024));
  return Buffer.concat(chunks);
}

function parseOctal(buffer) {
  const text = buffer.toString('ascii').replace(/\0.*$/, '').trim();
  if (!text) return 0;
  if (!/^[0-7]+$/.test(text)) throw new Error('invalid tar numeric field');
  const value = Number.parseInt(text, 8);
  if (!Number.isSafeInteger(value) || value < 0) throw new Error('invalid tar numeric field');
  return value;
}

export function parseTar(data) {
  const members = new Map();
  let ended = false;
  for (let offset = 0; offset + 512 <= data.length; ) {
    const header = data.subarray(offset, offset + 512);
    offset += 512;
    if (header.every((byte) => byte === 0)) {
      if (
        offset + 512 > data.length ||
        !data.subarray(offset, offset + 512).every((byte) => byte === 0)
      ) {
        throw new Error('invalid tar termination');
      }
      ended = true;
      if (!data.subarray(offset + 512).every((byte) => byte === 0))
        throw new Error('data after tar termination');
      break;
    }
    if (members.size >= MAX_MEMBERS) throw new Error('too many checkpoint members');
    const stored = parseOctal(header.subarray(148, 156));
    const check = Buffer.from(header);
    check.fill(0x20, 148, 156);
    if (check.reduce((total, byte) => total + byte, 0) !== stored)
      throw new Error('invalid tar checksum');
    const namePart = header.subarray(0, 100).toString('utf8').replace(/\0.*$/, '');
    const prefix = header.subarray(345, 500).toString('utf8').replace(/\0.*$/, '');
    const rawName = prefix ? `${prefix}/${namePart}` : namePart;
    const name = safeName(rawName);
    if (members.has(name)) throw new Error(`duplicate entry ${name}`);
    const size = parseOctal(header.subarray(124, 136));
    const type = String.fromCharCode(header[156] || 48);
    if (type !== '0' && type !== '5') throw new Error(`unsupported tar member ${name}`);
    if (type !== '5' && rawName.endsWith('/')) throw new Error(`invalid file path ${name}`);
    if (type === '5' && size !== 0) throw new Error('directory has non-zero size');
    if (offset + size > data.length || size > MAX_PACKAGE_BYTES)
      throw new Error('checkpoint exceeds allowed size');
    members.set(name, {directory: type === '5', data: data.subarray(offset, offset + size)});
    offset += Math.ceil(size / 512) * 512;
  }
  if (!ended) throw new Error('missing tar termination');
  return members;
}
