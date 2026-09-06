export function forceDownload(url: string, fallbackName = "download"): void {
  const link = document.createElement("a");
  link.href = url;
  link.download = fallbackName;
  link.rel = "noopener";
  document.body.appendChild(link);
  link.click();
  link.remove();
}

export function baseName(name: string): string {
  const parts = name.split("/").filter(Boolean);
  return parts.pop() || "download";
}

export function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes <= 0) return "0 B";
  const units = ["B", "KB", "MB", "GB"];
  const order = Math.min(
    Math.floor(Math.log2(bytes) / 10),
    units.length - 1,
  );
  const value = bytes / 2 ** (10 * order);
  return `${value.toFixed(value >= 100 || order === 0 ? 0 : 1)} ${units[order]}`;
}