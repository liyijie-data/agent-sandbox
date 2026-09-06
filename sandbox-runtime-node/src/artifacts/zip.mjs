import crypto from 'node:crypto';

function crc32(data) {
  let crc = 0xffffffff;
  for (const byte of data) {
    crc ^= byte;
    for (let bit = 0; bit < 8; bit++) crc = (crc >>> 1) ^ (0xedb88320 & -(crc & 1));
  }
  return (crc ^ 0xffffffff) >>> 0;
}

function u16(value) {
  const b = Buffer.alloc(2);
  b.writeUInt16LE(value);
  return b;
}
function u32(value) {
  const b = Buffer.alloc(4);
  b.writeUInt32LE(value >>> 0);
  return b;
}

export function buildZip(entries) {
  const parts = [];
  const central = [];
  let offset = 0;
  for (const entry of entries) {
    const name = Buffer.from(entry.name, 'utf8');
    const data = Buffer.from(entry.data);
    const crc = crc32(data);
    const header = Buffer.concat([
      Buffer.from('PK\x03\x04', 'binary'),
      u16(20),
      u16(0x800),
      u16(0),
      u16(0),
      u16(0),
      u32(crc),
      u32(data.length),
      u32(data.length),
      u16(name.length),
      u16(0),
      name,
    ]);
    parts.push(header, data);
    central.push(
      Buffer.concat([
        Buffer.from('PK\x01\x02', 'binary'),
        u16(20),
        u16(20),
        u16(0x800),
        u16(0),
        u16(0),
        u16(0),
        u32(crc),
        u32(data.length),
        u32(data.length),
        u16(name.length),
        u16(0),
        u16(0),
        u16(0),
        u16(0),
        u32(0),
        u32(offset),
        name,
      ]),
    );
    offset += header.length + data.length;
  }
  const centralData = Buffer.concat(central);
  parts.push(
    centralData,
    Buffer.concat([
      Buffer.from('PK\x05\x06', 'binary'),
      u16(0),
      u16(0),
      u16(entries.length),
      u16(entries.length),
      u32(centralData.length),
      u32(offset),
      u16(0),
    ]),
  );
  return Buffer.concat(parts);
}

export function sha256(data) {
  return crypto.createHash('sha256').update(data).digest('hex');
}
