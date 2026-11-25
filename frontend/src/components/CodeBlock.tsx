import { Children, isValidElement, useMemo, useState, type ReactElement, type ReactNode } from "react";
import { Check, Copy } from "lucide-react";
import { highlight, langLabel } from "@/lib/highlight";

/** Plain text of a rendered markdown node (code blocks arrive as nested strings). */
function textOf(node: ReactNode): string {
  if (node == null || typeof node === "boolean") return "";
  if (typeof node === "string" || typeof node === "number") return String(node);
  if (Array.isArray(node)) return node.map(textOf).join("");
  if (isValidElement(node)) return textOf((node.props as { children?: ReactNode }).children);
  return "";
}

/** Copy with a ✓ for 1.5 s. Falls back to a note when the browser blocks the clipboard. */
export function CopyButton({
  text,
  label = "Copy message",
  done = "Copied",
  showText = false,
}: {
  text: string;
  label?: string;
  done?: string;
  showText?: boolean;
}) {
  const [state, setState] = useState<"idle" | "copied" | "denied">("idle");
  return (
    <>
      <button
        type="button"
        className={`chat-copy ${state === "copied" ? "is-done" : ""}`}
        title={state === "copied" ? done : label}
        aria-label={state === "copied" ? done : label}
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
        <span className="chat-copy-icon" key={state} aria-hidden>
          {state === "copied" ? <Check size={13} /> : <Copy size={13} />}
        </span>
        {showText && <span>{state === "copied" ? done : "Copy"}</span>}
      </button>
      {state === "denied" && (
        <small className="chat-turn-note" role="status">
          Copy was blocked. Select the text to copy it.
        </small>
      )}
    </>
  );
}

const MAX_HIGHLIGHT = 60000;

/** A fenced code block: language label, Copy, horizontal scroll and light colour. */
export function CodeBlock({ children }: { children?: ReactNode }) {
  const child = Children.toArray(children)[0] as ReactElement<{ className?: string; children?: ReactNode }> | undefined;
  const className = isValidElement(child) ? child.props.className ?? "" : "";
  const lang = /language-([\w#+.-]+)/.exec(className)?.[1] ?? "";
  const code = (isValidElement(child) ? textOf(child.props.children) : textOf(children)).replace(/\n$/, "");
  const tokens = useMemo(
    () => (code.length <= MAX_HIGHLIGHT ? highlight(code, lang) : [{ kind: "plain" as const, text: code }]),
    [code, lang],
  );
  return (
    <figure className="chat-code">
      <figcaption>
        <span>{langLabel(lang)}</span>
        <CopyButton text={code} label="Copy code" done="Copied" showText />
      </figcaption>
      {/* Focusable so keyboard users can scroll long lines sideways. */}
      <pre tabIndex={0} aria-label={`${langLabel(lang)} code`}>
        <code>
          {tokens.map((t, i) =>
            t.kind === "plain" ? t.text : (
              <span key={i} className={`tok-${t.kind}`}>
                {t.text}
              </span>
            ),
          )}
        </code>
      </pre>
    </figure>
  );
}
