export class RemoteToolError extends Error {
  constructor(type, message, details = {}) {
    super(message);
    this.name = type;
    this.type = type;
    Object.assign(this, details);
  }
}

export function invalid(message = 'invalid tool configuration') {
  return new RemoteToolError('ToolConfigError', message);
}

export function transport(message = 'remote tool transport failed', status) {
  return new RemoteToolError('ToolTransportError', message, {status});
}
