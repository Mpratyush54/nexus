/**
 * Small notes at the bottom of the screen: "Chat archived · Undo".
 * They stay 5 s (8 s with Undo); components/Toasts.tsx draws them.
 */
export type Toast = { id: number; text: string; undo?: () => void; tone?: "error" };

let toasts: Toast[] = [];
export const currentToasts = () => toasts;
let seq = 0;
export const listeners = new Set<() => void>();
const timers = new Map<number, ReturnType<typeof setTimeout>>();

function emit() {
  for (const l of listeners) l();
}

export function dismissToast(id: number) {
  clearTimeout(timers.get(id));
  timers.delete(id);
  toasts = toasts.filter((t) => t.id !== id);
  emit();
}

export function showToast(text: string, opts: { undo?: () => void; tone?: "error" } = {}) {
  const id = ++seq;
  // At most three at once; the oldest leaves first.
  toasts = [...toasts, { id, text, ...opts }].slice(-3);
  timers.set(id, setTimeout(() => dismissToast(id), opts.undo ? 8000 : 5000));
  emit();
  return id;
}

