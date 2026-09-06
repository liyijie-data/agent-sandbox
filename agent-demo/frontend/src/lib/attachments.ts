import type { AttachmentMeta } from "../api/types";

export function parseAttachments(value: unknown): AttachmentMeta[] {
  try {
    const items = typeof value === "string" ? JSON.parse(value) : value;
    const candidates = Array.isArray(items)
      ? items
      : items && typeof items === "object" && Array.isArray((items as { attachments?: unknown }).attachments)
        ? (items as { attachments: unknown[] }).attachments
        : [];
    return candidates.filter(
      (item): item is AttachmentMeta =>
        item &&
        typeof item.id === "string" &&
        typeof item.name === "string" &&
        typeof item.kind === "string",
    );
  } catch {
    return [];
  }
}
