import { useEffect, useState } from "react";
import { ChevronRight, Sparkles } from "lucide-react";
import {
  thinkingHeader,
  thinkingLive,
  thinkingMs,
  thinkingPreview,
  type ThinkingJob,
} from "@/lib/thinking";

/**
 * A reply's thinking: "Thinking… 4s" with a shimmer and the latest line
 * while the agent thinks, then a quiet "Thought for 7s ▸" that opens to the
 * whole text. Renders nothing when the job has no thinking; mount it once
 * the job has some, so its clock starts with the thinking.
 */
export function ThinkingBlock({ job }: { job: ThinkingJob }) {
  const live = thinkingLive(job);
  // When this page first showed the thinking (the block mounts with it), to
  // count while the desktop's own measure catches up.
  const [seenSince] = useState(() => Date.now());
  const [now, setNow] = useState(() => Date.now());
  const [open, setOpen] = useState(false);
  useEffect(() => {
    if (!live) return;
    const t = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(t);
  }, [live]);
  if (!job.thinking?.trim()) return null;
  const header = thinkingHeader(live, thinkingMs(job, seenSince, now));
  return (
    <div className={`chat-thinking ${live ? "is-live" : ""}`}>
      <details onToggle={(e) => setOpen(e.currentTarget.open)}>
        <summary>
          <ChevronRight size={13} className="chat-chevron" aria-hidden />
          <Sparkles size={13} aria-hidden />
          <span className="chat-thinking-label" role={live ? "status" : undefined}>
            {header}
          </span>
        </summary>
        <div className="chat-thinking-body">{job.thinking.trim()}</div>
      </details>
      {live && !open && (
        <p className="chat-thinking-preview" aria-hidden="true">
          {thinkingPreview(job.thinking)}
        </p>
      )}
    </div>
  );
}
