import { useEffect, useSyncExternalStore } from "react";
import { X } from "lucide-react";
import { isMod } from "@/lib/shortcuts";
import { currentToasts, dismissToast, listeners } from "@/lib/toasts";

/**
 * Small notes at the bottom of the screen: "Chat archived · Undo".
 * They slide up, stay 5 s (8 s with Undo), and Ctrl/Cmd+Z undoes the newest.
 */
export function ToastHost() {
  const items = useSyncExternalStore(
    (l) => {
      listeners.add(l);
      return () => listeners.delete(l);
    },
    currentToasts,
    currentToasts,
  );
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (!isMod(e) || e.key.toLowerCase() !== "z" || e.shiftKey) return;
      const target = e.target as HTMLElement | null;
      // Ctrl+Z in a text box is the box's own undo.
      if (target?.closest("input, textarea, [contenteditable]")) return;
      const last = [...currentToasts()].reverse().find((t) => t.undo);
      if (!last) return;
      e.preventDefault();
      last.undo?.();
      dismissToast(last.id);
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);
  return (
    <div className="nx-toasts" role="status" aria-live="polite">
      {items.map((t) => (
        <div key={t.id} className={`nx-toast ${t.tone === "error" ? "is-error" : ""}`}>
          <span>{t.text}</span>
          {t.undo && (
            <button
              type="button"
              className="nx-toast-undo"
              onClick={() => {
                t.undo?.();
                dismissToast(t.id);
              }}
            >
              Undo
            </button>
          )}
          <button type="button" className="nx-toast-close" aria-label="Dismiss" onClick={() => dismissToast(t.id)}>
            <X size={14} />
          </button>
        </div>
      ))}
    </div>
  );
}
