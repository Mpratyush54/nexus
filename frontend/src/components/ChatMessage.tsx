import { useState } from "react";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import { ChevronRight, Copy } from "lucide-react";
import type { ChatTurn } from "@/api/chats";
import { readableChatText, toolViews } from "@/lib/chat-content";
import { ChatDiff } from "./ChatDiff";

export function ChatMessage({
  turn,
  harness,
  match = "",
}: {
  turn: ChatTurn;
  harness: string;
  match?: string;
}) {
  const text = readableChatText(turn.text_preview ?? "");
  const tools = toolViews(turn.tool_calls);
  const [copyError, setCopyError] = useState(false);
  if (!text && tools.length === 0) return null;
  const isUser = turn.role === "user";
  const at = new Date(turn.source_ts || turn.ts || "");
  return (
    <article
      id={`turn-${turn.cursor_idx || turn.idx}-${turn.role}`}
      className={`chat-turn ${isUser ? "is-user" : "is-agent"} ${match && text.toLocaleLowerCase().includes(match.toLocaleLowerCase()) ? "is-match" : ""}`}
    >
      <div className="chat-turn-role">
        <span>{isUser ? "You" : harness || "Agent"}</span>
        {Number.isFinite(at.getTime()) && (
          <time dateTime={at.toISOString()}>
            {at.toLocaleString(undefined, {
              month: "short",
              day: "numeric",
              hour: "2-digit",
              minute: "2-digit",
            })}
          </time>
        )}
        {text && (
          <button
            type="button"
            title="Copy message"
            aria-label="Copy message"
            onClick={() => {
              void navigator.clipboard
                .writeText(text)
                .catch(() => setCopyError(true));
            }}
          >
            <Copy size={13} />
          </button>
        )}
      </div>
      {text && (
        <div className="chat-prose">
          <ReactMarkdown
            remarkPlugins={[remarkGfm]}
            components={{
              a: ({ href, children }) => (
                <a href={href} target="_blank" rel="noreferrer">
                  {children}
                </a>
              ),
              img: ({ src, alt }) => (
                <img
                  src={src}
                  alt={alt || "Conversation attachment"}
                  loading="lazy"
                  referrerPolicy="no-referrer"
                />
              ),
            }}
          >
            {text}
          </ReactMarkdown>
        </div>
      )}
      {copyError && (
        <small>
          Clipboard permission was denied. You can select and copy the message.
        </small>
      )}
      {tools.length > 0 && (
        <div className="chat-tool-list">
          {tools.map((tool, i) =>
            tool.diff || tool.name === "workspace_change" ? (
              <ChatDiff
                key={i}
                path={tool.path}
                diff={tool.diff}
                notice={tool.notice}
              />
            ) : (
              <details className="chat-tool" key={i}>
                <summary>
                  <ChevronRight size={13} />
                  <span>{tool.name}</span>
                  <code>{tool.command || tool.path || "View details"}</code>
                </summary>
                <div className="chat-tool-details">
                  {tool.command && (
                    <>
                      <small>Command</small>
                      <pre>{tool.command}</pre>
                    </>
                  )}
                  {tool.input && tool.input !== "{}" && (
                    <>
                      <small>Input</small>
                      <pre>{tool.input}</pre>
                    </>
                  )}
                  {tool.output && (
                    <>
                      <small>Output</small>
                      <pre>{tool.output}</pre>
                    </>
                  )}
                  {!tool.output && (
                    <small>No output was captured for this tool.</small>
                  )}
                </div>
              </details>
            ),
          )}
        </div>
      )}
    </article>
  );
}
