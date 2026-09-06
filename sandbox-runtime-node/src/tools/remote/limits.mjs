export const LIMITS = Object.freeze({
  requestBytes: 4 * 1024 * 1024,
  responseBytes: 4 * 1024 * 1024,
  specBytes: 8 * 1024 * 1024,
  schemaBytes: 128 * 1024,
  maxRefDepth: 16,
  maxOperations: 1024,
  maxTools: 512,
  timeoutMs: 30_000,
});

export function deadlineMs(deadline) {
  if (typeof deadline === 'function') return Math.max(1, Math.ceil(Number(deadline()) * 1000));
  if (deadline && typeof deadline.remaining === 'function') return Math.max(1, Math.ceil(Number(deadline.remaining()) * 1000));
  return LIMITS.timeoutMs;
}
