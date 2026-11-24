import { useState } from "react";
import { Check, Copy, FileCode2 } from "lucide-react";
import { diffRows } from "@/lib/chat-content";

export function ChatDiff({
  path,
  diff,
  notice,
}: {
  path: string;
  diff: string;
  notice?: string;
}) {
  const [copied, setCopied] = useState(false);
  const [error, setError] = useState(false);
  const rows = diffRows(diff);
  const added = rows.filter((row) => row.kind === "added").length;
  const removed = rows.filter((row) => row.kind === "removed").length;
  return (
    <section className="chat-diff" aria-label={`Changes in ${path}`}>
      <header>
        <FileCode2 size={15} />
        <strong>{path || "File change"}</strong>
        {diff && (
          <span className="chat-diff-stats">
            <span>+{added}</span>
            <span>−{removed}</span>
          </span>
        )}
        <button
          type="button"
          aria-label="Copy diff"
          disabled={!diff}
          onClick={() => {
            void navigator.clipboard
              .writeText(diff)
              .then(() => setCopied(true))
              .catch(() => setError(true));
          }}
        >
          {copied ? <Check size={14} /> : <Copy size={14} />}
        </button>
      </header>
      {notice && <p className="chat-diff-notice">{notice}</p>}
      {error && (
        <p className="chat-diff-notice">
          Clipboard permission denied. Select the diff to copy it.
        </p>
      )}
      {diff ? (
        <pre className="chat-diff-body" tabIndex={0}>
          <code>
            {rows.map((row, i) => (
              <span key={i} className={`diff-${row.kind}`}>
                <span className="diff-line-number" aria-hidden>
                  {row.before ?? ""}
                </span>
                <span className="diff-line-number" aria-hidden>
                  {row.after ?? ""}
                </span>
                {row.text}
                {"\n"}
              </span>
            ))}
          </code>
        </pre>
      ) : (
        <p className="chat-diff-notice">
          {notice ? "" : "No patch was saved for this earlier file change."}
        </p>
      )}
    </section>
  );
}
