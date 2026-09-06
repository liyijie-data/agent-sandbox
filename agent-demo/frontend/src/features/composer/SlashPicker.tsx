export interface PickerItem {
  id: string;
  name: string;
  kind: "skill" | "tool";
  selected: boolean;
}

import styles from "./composer.module.css";

interface SlashPickerProps {
  open: boolean;
  scope: "all" | "skill" | "tool";
  query: string;
  items: PickerItem[];
  onQueryChange: (query: string) => void;
  onToggle: (item: PickerItem) => void;
  onClose: () => void;
  onEnter: () => void;
}

export default function SlashPicker({
  open,
  scope,
  query,
  items,
  onQueryChange,
  onToggle,
  onClose,
  onEnter,
}: SlashPickerProps) {
  if (!open) return null;

  const scoped = items.filter((item) =>
    scope === "all" ? true : item.kind === scope,
  );
  const visible = scoped.filter((item) =>
    item.name.toLowerCase().includes(query.toLowerCase()),
  );

  return (
    <div className={styles.picker} role="dialog" aria-label="选择 Skill 或工具">
      <input
        autoFocus
        className={styles.pickerSearch}
        placeholder="搜索 Skill 或工具…"
        value={query}
        onChange={(e) => onQueryChange(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === "Enter") {
            e.preventDefault();
            onEnter();
          } else if (e.key === "Escape") {
            e.preventDefault();
            onClose();
          }
        }}
      />
      <div className={styles.pickerList} role="listbox">
        {visible.length === 0 ? (
          <div className={styles.pickerEmpty}>没有匹配的资源</div>
        ) : (
          visible.map((item) => (
            <button
              key={item.kind + item.id}
              type="button"
              className={styles.pickerOption}
              role="option"
              aria-selected={item.selected}
              onClick={() => onToggle(item)}
            >
              <span
                className={[
                  styles.checkbox,
                  item.selected ? styles.checkboxOn : "",
                ].join(" ")}
              />
              <span className={styles.pickerKind}>
                {item.kind === "skill" ? "Skill" : "工具"}
              </span>
              <span className={styles.pickerName}>{item.name}</span>
            </button>
          ))
        )}
      </div>
    </div>
  );
}