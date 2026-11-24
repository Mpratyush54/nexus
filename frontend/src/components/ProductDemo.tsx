import { useState } from "react";
import { Check, GitFork, MessageSquare, FileCode2, Wrench } from "lucide-react";
import { ChatMessage } from "./ChatMessage";
import { ChatDiff } from "./ChatDiff";

// Clearly labelled fixtures using the real conversation components.
export const demoDiff =
  "@@ -12,4 +12,5 @@\n export async function restoreSession(session) {\n-  return loadMessages(session.id)\n+  const context = await loadSharedContext(session.id)\n+  return resumeWithContext(session, context)\n }";
export function ProductDemo({
  initialView = "chat",
  controls = true,
  film = false,
}: {
  initialView?: string;
  controls?: boolean;
  film?: boolean;
}) {
  const [view, setView] = useState(initialView);
  return (
    <div className="product-demo conversation-layout" data-product-demo>
      <header className="product-demo-header">
        <div>
          <span className="product-demo-dot" />
          team-workspace <span>/</span> Keep the context
        </div>
        <span>
          <GitFork size={14} />
          Shared context
        </span>
      </header>
      {controls && (
        <nav aria-label="Explore the product demo">
          {[
            { id: "chat", label: "Conversation", Icon: MessageSquare },
            { id: "activity", label: "Tool activity", Icon: Wrench },
            { id: "changes", label: "File changes", Icon: FileCode2 },
          ].map(({ id, label, Icon }) => (
            <button
              key={id}
              aria-pressed={view === id}
              onClick={() => setView(id)}
            >
              <Icon size={14} />
              {label}
            </button>
          ))}
        </nav>
      )}
      <div className="product-demo-content">
        {view === "chat" && (
          <>
            <ChatMessage
              harness="Codex"
              turn={{
                idx: 1,
                role: "user",
                text_preview: film
                  ? "Continue the session restore work."
                  : "Can we continue the work without losing the decisions from the previous chat?",
              }}
            />
            <ChatMessage
              harness="Codex"
              turn={{
                idx: 2,
                role: "assistant",
                text_preview: film
                  ? "Context loaded. The patch is ready."
                  : "The shared context includes the decision to keep session history and workspace changes together.\n\nI updated the restore flow so the next agent can pick up that context—not just a transcript.",
              }}
            />
            <div className="product-demo-result">
              <Check size={14} />
              Conversation and code changes, together.
            </div>
          </>
        )}
        {view === "activity" && (
          <ChatMessage
            harness="Codex"
            turn={{
              idx: 3,
              role: "assistant",
              text_preview: film
                ? "Tests passed."
                : "Here is the recorded activity for this example.",
              tool_calls: film
                ? [
                    {
                      name: "shell",
                      command: "npm test -- session",
                      output: "Session restore tests passed.",
                    },
                  ]
                : [
                    {
                      name: "read",
                      args: { path: "src/session.ts" },
                      output: "Loaded the existing session restore flow.",
                    },
                    {
                      name: "shell",
                      args: { command: "npm test -- session" },
                      output: "Session restore tests passed.",
                    },
                  ],
            }}
          />
        )}
        {view === "changes" && (
          <ChatDiff path="src/session.ts" diff={demoDiff} />
        )}
      </div>
      <footer>Illustrative project · Nexus message and diff components</footer>
    </div>
  );
}
