export function encodeName(value) {
  let out = '';
  for (const byte of Buffer.from(String(value), 'utf8')) {
    const ch = String.fromCharCode(byte);
    if ((byte >= 0x41 && byte <= 0x5a) || (byte >= 0x61 && byte <= 0x7a) || (byte >= 0x30 && byte <= 0x39) || ch === '-') out += ch;
    else if (ch === '_') out += ch;
    else out += `_x${byte.toString(16).padStart(2, '0')}`;
  }
  return out;
}

export function createNameAllocator(reserved = []) {
  const used = new Set(reserved);
  return {
    allocate(value) {
      const base = encodeName(value);
      for (let index = 0; ; index++) {
        const suffix = index === 0 ? '' : `_${index}`;
        const name = `${base.slice(0, 64 - suffix.length)}${suffix}`;
        if (!used.has(name)) {
          used.add(name);
          return name;
        }
      }
    },
  };
}

export function toolName(_id, operation, allocator = createNameAllocator()) {
  return allocator.allocate(operation);
}
