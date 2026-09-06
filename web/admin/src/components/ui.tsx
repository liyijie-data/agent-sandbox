import type { ReactNode } from "react";

export function Badge({ value }: { value: string }) {
  return <span className={`badge ${value}`}>{value}</span>;
}

export function ErrorBox({ error }: { error: unknown }) {
  if (!error) return null;
  const message = error instanceof Error ? error.message : String(error);
  return <div className="error-box">{message}</div>;
}

export function OkBox({ text }: { text: string }) {
  if (!text) return null;
  return <div className="ok-box">{text}</div>;
}

export function Field({
  label,
  children,
}: {
  label: string;
  children: ReactNode;
}) {
  return (
    <div className="field-wrap">
      <label className="field-label">{label}</label>
      <div>{children}</div>
    </div>
  );
}

export function fmtTime(value: string | null | undefined): string {
  if (!value) return "—";
  return value.replace("T", " ").slice(0, 19);
}
