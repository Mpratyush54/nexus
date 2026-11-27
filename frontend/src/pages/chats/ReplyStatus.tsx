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
  quiet = false,
}: {
  verb: string;
  startedAt: number;
  touch: boolean;
  /**
   * The live thinking block above says "Thinking… 4s": the line keeps only
   * its dot and how to stop, so the reply never says it twice.
   */
  quiet?: boolean;
}) {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (quiet) return;
    const t = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(t);
  }, [quiet]);
  if (quiet && touch) return null;
  return (
    <div className={`chat-reply-status ${quiet ? "is-quiet" : ""}`}>
      <span className="chat-reply-dot" aria-hidden="true" />
      <span className="chat-reply-verb" role="status">
        {quiet ? "" : verb}
      </span>
      <span className="chat-reply-meta" aria-hidden="true">
        {quiet ? (
          <>
            <kbd>Esc</kbd> to stop
          </>
        ) : (
          <>
            {elapsedLabel(now - startedAt)}
            {!touch && <> · <kbd>Esc</kbd> to stop</>}
          </>
        )}
      </span>
    </div>
  );
}
