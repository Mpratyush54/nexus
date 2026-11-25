import { useState } from "react";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import {
  Bell,
  Bot,
  Check,
  ChevronRight,
  Copy,
  FileText,
  History,
  Image as ImageIcon,
  Wrench,
} from "lucide-react";
import type { ChatTurn } from "@/api/chats";
import { ChatImages, type ChatImage } from "./ChatImages";
import { toolViews, type ToolView } from "@/lib/chat-content";
import {
  agentName,
  parseTurnText,
  type Envelope,
} from "@/lib/harness-message";
import { ChatDiff } from "./ChatDiff";

const markdownComponents = {
  a: ({ href, children }: { href?: string; children?: React.ReactNode }) => (
    <a href={href} target="_blank" rel="noreferrer">
      {children}
    </a>
  ),
  // react-markdown drops data: URLs, so a base64 image in a message is
  // never decoded here; capture keeps image bytes off the server anyway.
  img: ({ src, alt }: { src?: string | Blob; alt?: string }) => (
    <img
      src={typeof src === "string" ? src : undefined}
      alt={alt || "Conversation attachment"}
      loading="lazy"
      decoding="async"
      referrerPolicy="no-referrer"
    />
  ),
};

function Markdown({ children }: { children: string }) {
  return (
    <div className="chat-prose">
      <ReactMarkdown remarkPlugins={[remarkGfm]} components={markdownComponents}>
        {children}
      </ReactMarkdown>
    </div>
  );
}

/** True when a stored turn has nothing a reader needs (harness-only text, no tools). */
export function isSilentTurn(turn: ChatTurn): boolean {
  const parsed = parseTurnText(turn.text_preview ?? "");
  return (
    !parsed.text &&
    !parsed.envelopes.length &&
    !parsed.compaction &&
    !parsed.attachments.length &&
    toolViews(turn.tool_calls).length === 0
  );
}

/** True for an agent turn that only ran tools: it belongs in a work log. */
export function isToolOnlyTurn(turn: ChatTurn): boolean {
  if (turn.role === "user") return false;
  const parsed = parseTurnText(turn.text_preview ?? "");
  return !parsed.text && !parsed.envelopes.length && toolViews(turn.tool_calls).length > 0;
}

function turnTime(turn: ChatTurn) {
  const at = new Date(turn.source_ts || turn.ts || "");
  return Number.isFinite(at.getTime()) ? at : null;
}

function TimeStamp({ at }: { at: Date | null }) {
  if (!at) return null;
  return (
    <time dateTime={at.toISOString()}>
      {at.toLocaleString(undefined, {
        month: "short",
        day: "numeric",
        hour: "2-digit",
        minute: "2-digit",
      })}
    </time>
  );
}

function CopyButton({ text }: { text: string }) {
  const [state, setState] = useState<"idle" | "copied" | "denied">("idle");
  return (
    <>
      <button
        type="button"
        title={state === "copied" ? "Copied" : "Copy message"}
        aria-label={state === "copied" ? "Message copied" : "Copy message"}
        onClick={() => {
          void navigator.clipboard
            .writeText(text)
            .then(() => {
              setState("copied");
              setTimeout(() => setState("idle"), 1500);
            })
            .catch(() => setState("denied"));
        }}
      >
        {state === "copied" ? <Check size={13} /> : <Copy size={13} />}
      </button>
      {state === "denied" && (
        <small className="chat-turn-note" role="status">
          Copy was blocked. Select the text to copy it.
        </small>
      )}
    </>
  );
}

function EnvelopeCard({ envelope }: { envelope: Envelope }) {
  const Icon = envelope.kind === "task" ? Bell : Bot;
  const failed = ["failed", "error", "killed"].includes(envelope.status);
  return (
    <details
      className={`chat-envelope ${failed ? "is-error" : ""}`}
      open={envelope.kind !== "task" && envelope.body.length < 600}
    >
      <summary>
        <ChevronRight size={13} className="chat-chevron" aria-hidden />
        <Icon size={14} aria-hidden />
        <span className="chat-envelope-label">{envelope.label}</span>
        <span className="chat-envelope-title">{envelope.title}</span>
        {envelope.status && (
          <span className="chat-envelope-status">{envelope.status}</span>
        )}
      </summary>
      {envelope.body && <Markdown>{envelope.body}</Markdown>}
    </details>
  );
}

function ToolStep({ tool }: { tool: ToolView }) {
  if (tool.diff || tool.name === "workspace_change")
    return <ChatDiff path={tool.path} diff={tool.diff} notice={tool.notice} />;
  if (tool.subagent)
    return (
      <details className="chat-subagent">
        <summary>
          <ChevronRight size={13} className="chat-chevron" aria-hidden />
          <Bot size={14} aria-hidden />
          <span className="chat-envelope-label">Sub-agent</span>
          <span className="chat-envelope-title">{tool.subagent}</span>
        </summary>
        <div className="chat-subagent-body">
          {tool.brief ? (
            <>
              <p className="chat-subagent-heading">What it was asked</p>
              <Markdown>{tool.brief}</Markdown>
            </>
          ) : null}
          <p className="chat-subagent-heading">What it found</p>
          {tool.output ? (
            <Markdown>{tool.output}</Markdown>
          ) : (
            <p className="chat-turn-note">No report was captured.</p>
          )}
        </div>
      </details>
    );
  return (
    <details className="chat-tool">
      <summary>
        <ChevronRight size={13} className="chat-chevron" aria-hidden />
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
        {tool.input && tool.input !== "{}" && !tool.command && (
          <>
            <small>Input</small>
            <pre>{tool.input}</pre>
          </>
        )}
        {tool.output ? (
          <>
            <small>Output</small>
            <pre>{tool.output}</pre>
          </>
        ) : (
          <small>No output was captured.</small>
        )}
      </div>
    </details>
  );
}

function ToolList({ tools }: { tools: ToolView[] }) {
  return (
    <div className="chat-tool-list">
      {tools.map((tool, i) => (
        <ToolStep key={i} tool={tool} />
      ))}
    </div>
  );
}

/** Consecutive tool-only agent turns, shown as one collapsible work log. */
export function ChatWorkLog({
  turns,
  harness,
}: {
  turns: ChatTurn[];
  harness: string;
}) {
  const tools = turns.flatMap((turn) => toolViews(turn.tool_calls));
  const changes = tools.filter((tool) => tool.diff || tool.name === "workspace_change");
  const steps = tools.filter((tool) => !(tool.diff || tool.name === "workspace_change"));
  const first = turns[0];
  return (
    <article
      id={`turn-${first.cursor_idx || first.idx}-${first.role}`}
      className="chat-turn is-agent is-worklog"
    >
      <div className="chat-turn-role">
        <span>{agentName(harness)}</span>
        <TimeStamp at={turnTime(first)} />
      </div>
      {steps.length > 0 && (
        <details className="chat-worklog">
          <summary>
            <ChevronRight size={13} className="chat-chevron" aria-hidden />
            <Wrench size={13} aria-hidden />
            <span>
              Worked through {steps.length} {steps.length === 1 ? "step" : "steps"}
            </span>
            <code>
              {steps
                .slice(0, 3)
                .map((tool) => tool.subagent || tool.name)
                .join(" · ")}
              {steps.length > 3 ? " …" : ""}
            </code>
          </summary>
          <ToolList tools={steps} />
        </details>
      )}
      {changes.length > 0 && <ToolList tools={changes} />}
    </article>
  );
}

export function ChatMessage({
  turn,
  harness,
  match = "",
  images = [],
}: {
  turn: ChatTurn;
  harness: string;
  match?: string;
  /** Images this browser just sent with the message (shown until the chat syncs). */
  images?: ChatImage[];
}) {
  const parsed = parseTurnText(turn.text_preview ?? "");
  const tools = toolViews(turn.tool_calls);
  const { text, envelopes, attachments, context, compaction } = parsed;
  if (!text && !tools.length && !envelopes.length && !compaction && !attachments.length && !images.length)
    return null;
  // A user turn made only of envelopes was written by the harness, not the person.
  const isUser = turn.role === "user" && (!!text || attachments.length > 0 || images.length > 0);
  const matched =
    !!match && text.toLocaleLowerCase().includes(match.toLocaleLowerCase());
  return (
    <article
      id={`turn-${turn.cursor_idx || turn.idx}-${turn.role}`}
      className={`chat-turn ${isUser ? "is-user" : "is-agent"} ${matched ? "is-match" : ""}`}
    >
      <div className="chat-turn-role">
        <span>{isUser ? "You" : agentName(harness)}</span>
        <TimeStamp at={turnTime(turn)} />
        {text && <CopyButton text={text} />}
      </div>
      {compaction && (
        <details className="chat-envelope is-summary">
          <summary>
            <ChevronRight size={13} className="chat-chevron" aria-hidden />
            <History size={14} aria-hidden />
            <span className="chat-envelope-label">Earlier conversation summarized</span>
          </summary>
          <Markdown>{compaction}</Markdown>
        </details>
      )}
      {envelopes.map((envelope, i) => (
        <EnvelopeCard key={i} envelope={envelope} />
      ))}
      {text && <Markdown>{text}</Markdown>}
      {images.length > 0 && <ChatImages images={images} />}
      {attachments.length > 0 && (
        <ul className="chat-attachments" aria-label="Attachments">
          {attachments.map((file) => {
            const label = file.name || file.path.split(/[\\/]/).pop() || "Attachment";
            // Image bytes are never uploaded with a chat: the picture stays
            // on the computer it was sent from, so the web shows a chip.
            return (
              <li
                key={file.path || file.name}
                title={file.isImage ? "Image kept on the computer it was sent from" : file.path}
              >
                {file.isImage ? <ImageIcon size={13} aria-hidden /> : <FileText size={13} aria-hidden />}
                {label}
              </li>
            );
          })}
        </ul>
      )}
      {context.length > 0 && (
        <details className="chat-context">
          <summary>
            <ChevronRight size={12} className="chat-chevron" aria-hidden />
            Context sent with this message
          </summary>
          <pre>{context.join("\n\n")}</pre>
        </details>
      )}
      {tools.length > 0 && <ToolList tools={tools} />}
    </article>
  );
}
