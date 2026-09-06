import {invalid} from './errors.mjs';

const NAME = /^[A-Za-z0-9!#$%&'*+.^_`|~-]+$/;
const FORBIDDEN = new Set([
  'host', 'connection', 'transfer-encoding', 'proxy-authorization', 'proxy-connection',
  'via', 'te', 'trailer', 'upgrade', 'keep-alive', 'content-length', 'content-type',
  'accept', 'forwarded', 'x-forwarded-for', 'x-forwarded-host', 'x-forwarded-proto',
  'x-forwarded-port', 'x-real-ip',
]);

export function accessHeaders(access = {type: 'none'}) {
  if (!access || typeof access !== 'object' || Array.isArray(access)) throw invalid('access must be an object');
  const type = access.type || 'none';
  if (type === 'none') return {};
  if (type === 'bearer') {
    if (typeof access.token !== 'string' || !access.token || /[\r\n]/.test(access.token)) throw invalid('invalid bearer access');
    return {authorization: `Bearer ${access.token}`};
  }
  if (type !== 'headers' || !access.headers || typeof access.headers !== 'object') throw invalid('unsupported access type');
  const out = {};
  for (const [name, value] of Object.entries(access.headers)) {
    if (!NAME.test(name) || FORBIDDEN.has(name.toLowerCase()) || typeof value !== 'string' || /[\r\n]/.test(value)) throw invalid('invalid access header');
    out[name] = value;
  }
  if (!Object.keys(out).length) throw invalid('headers access is empty');
  return out;
}

export {FORBIDDEN};
