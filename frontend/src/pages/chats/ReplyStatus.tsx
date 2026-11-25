import { useEffect, useState } from "react";

function elapsedLabel(ms: number): string {
  const secs = Math.max(0, Math.floor(ms / 1000));
  if (secs < 60) return `${secs}s`;
  return `${Math.floor(secs / 60)}m ${String(secs % 60).padStart(2, "0")}s`;
}

/**
 * The line inside a reply while the agent works: what it's doing, a clock
 * that ticks every second, and how to stop it. Screen readers hear the
 * step changes, not every tick.
 */
export function ReplyStatus({
  verb,
  startedAt,
  touch,
}: {
  verb: string;
  startedAt: number;
  touch: boolean;
}) {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const t = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(t);
  }, []);
  return (
    <div className="chat-reply-status">
      <span className="chat-reply-dot" aria-hidden="true" />
      <span className="chat-reply-verb" role="status">
        {verb}
      </span>
      <span className="chat-reply-meta" aria-hidden="true">
        {elapsedLabel(now - startedAt)}
        {!touch && <> · <kbd>Esc</kbd> to stop</>}
      </span>
    </div>
  );
}
