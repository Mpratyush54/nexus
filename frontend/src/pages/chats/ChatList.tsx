import { useEffect, useRef, useState } from "react";
import {
  Archive,
  ArchiveRestore,
  GitFork,
  Link2,
  MoreHorizontal,
  Pencil,
  Pin,
  PinOff,
  Trash2,
} from "lucide-react";
import type { ChatCard } from "@/api/chats";
import { readableChatText } from "@/lib/chat-content";
import { agentName, chatTitle } from "@/lib/harness-message";
import { chatStatus, type ChatStatus } from "@/lib/live-chats";
import { hasDraft } from "@/lib/drafts";
import { formatRelative } from "@/utils/format";

export type RowCommand = "pin" | "rename" | "link" | "archive" | "delete";

type MenuItem = { command: RowCommand; label: string; icon: typeof Pin; danger?: boolean };

/** The ⋯ / right-click menu on a chat in the list. Arrow keys move, Esc closes. */
function RowMenu({
  items,
  at,
  onPick,
  onClose,
}: {
  items: MenuItem[];
  at: { x: number; y: number } | null;
  onPick: (command: RowCommand) => void;
  onClose: (refocus: boolean) => void;
}) {
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    ref.current?.querySelector<HTMLButtonElement>("[role=menuitem]")?.focus();
    const onDown = (e: PointerEvent) => {
      if (!ref.current?.contains(e.target as Node)) onClose(false);
    };
    document.addEventListener("pointerdown", onDown, true);
    return () => document.removeEventListener("pointerdown", onDown, true);
  }, [onClose]);
  return (
    <div
      ref={ref}
      className={`chat-row-menu ${at ? "is-at-pointer" : ""}`}
      style={at ? { left: at.x, top: at.y } : undefined}
      role="menu"
      aria-label="Chat actions"
      onKeyDown={(e) => {
        const buttons = [...(ref.current?.querySelectorAll<HTMLButtonElement>("[role=menuitem]") ?? [])];
        const at = buttons.indexOf(document.activeElement as HTMLButtonElement);
        if (e.key === "ArrowDown" || e.key === "ArrowUp") {
          e.preventDefault();
          const next = (at + (e.key === "ArrowDown" ? 1 : buttons.length - 1)) % buttons.length;
          buttons[next]?.focus();
        } else if (e.key === "Home" || e.key === "End") {
          e.preventDefault();
          buttons[e.key === "Home" ? 0 : buttons.length - 1]?.focus();
        } else if (e.key === "Escape" || e.key === "Tab") {
          e.preventDefault();
          e.stopPropagation();
          onClose(true);
        }
      }}
    >
      {items.map((item) => (
        <button
          key={item.command}
          type="button"
          role="menuitem"
          className={item.danger ? "is-danger" : ""}
          onClick={() => onPick(item.command)}
        >
          <item.icon size={14} aria-hidden />
          {item.label}
        </button>
      ))}
    </div>
  );
}

export function ChatRow({
  card,
  title,
  active,
  pinned,
  archived,
  live,
  onOpen,
  onCommand,
  onRename,
}: {
  card: ChatCard;
  title?: string;
  active: boolean;
  pinned: boolean;
  archived: boolean;
  live: Parameters<typeof chatStatus>[1];
  onOpen: () => void;
  onCommand: (command: RowCommand) => void;
  onRename: (title: string) => void;
}) {
  const [menu, setMenu] = useState<null | { at: { x: number; y: number } | null }>(null);
  const [renaming, setRenaming] = useState(false);
  const moreRef = useRef<HTMLButtonElement>(null);
  // Esc cancels: the blur that follows must not save.
  const renameDone = useRef(false);
  const finishRename = (value: string | null) => {
    if (renameDone.current) return;
    renameDone.current = true;
    if (value !== null) onRename(value);
    setRenaming(false);
  };
  const startRename = () => {
    renameDone.current = false;
    setRenaming(true);
  };
  const name = title || chatTitle(card.title, card.summary, card.harness);
  const status: ChatStatus | null = chatStatus(card.last_active_at, live);
  const draft = !active && hasDraft(card.session_id);
  const items: MenuItem[] = [
    { command: "pin", label: pinned ? "Unpin" : "Pin", icon: pinned ? PinOff : Pin },
    { command: "rename", label: "Rename…", icon: Pencil },
    { command: "link", label: "Copy link", icon: Link2 },
    { command: "archive", label: archived ? "Unarchive" : "Archive", icon: archived ? ArchiveRestore : Archive },
    { command: "delete", label: "Delete…", icon: Trash2, danger: true },
  ];
  return (
    <div
      className={`conversation-row ${menu ? "is-menu-open" : ""}`}
      onContextMenu={(e) => {
        e.preventDefault();
        const box = e.currentTarget.getBoundingClientRect();
        setMenu({ at: { x: e.clientX - box.left, y: e.clientY - box.top } });
      }}
    >
      {renaming ? (
        <form
          className="conversation-card is-renaming"
          onSubmit={(e) => {
            e.preventDefault();
            finishRename(String(new FormData(e.currentTarget).get("title") ?? ""));
          }}
        >
          <input
            name="title"
            defaultValue={name}
            aria-label="Chat name"
            autoFocus
            maxLength={120}
            onFocus={(e) => e.currentTarget.select()}
            onBlur={(e) => finishRename(e.currentTarget.value)}
            onKeyDown={(e) => {
              if (e.key === "Escape") {
                e.preventDefault();
                e.stopPropagation();
                finishRename(null);
              }
            }}
          />
        </form>
      ) : (
        <button
          className={`conversation-card ${active ? "is-active" : ""}`}
          aria-current={active ? "true" : undefined}
          onClick={onOpen}
          onKeyDown={(e) => {
            if (e.key === "F2") {
              e.preventDefault();
              startRename();
            }
          }}
        >
          <span>
            <strong>
              {pinned && <Pin size={11} className="conversation-pin" aria-label="Pinned" />}
              {name}
            </strong>
            {card.parent_session_id && <GitFork size={13} aria-label="Forked chat" />}
          </span>
          {draft ? (
            <p className="conversation-draft">Draft</p>
          ) : (
            card.summary && <p>{readableChatText(card.summary)}</p>
          )}
          <small>
            <span>
              {agentName(card.harness)}
              {status && <em className={`chat-status is-${status.tone}`}>{status.label}</em>}
            </span>
            <time dateTime={card.last_active_at}>{formatRelative(card.last_active_at)}</time>
          </small>
        </button>
      )}
      {!renaming && (
        <button
          ref={moreRef}
          type="button"
          className="conversation-row-more"
          aria-label={`More actions for ${name}`}
          title="More actions"
          aria-haspopup="menu"
          aria-expanded={!!menu}
          onClick={() => setMenu(menu ? null : { at: null })}
        >
          <MoreHorizontal size={15} />
        </button>
      )}
      {menu && (
        <RowMenu
          items={items}
          at={menu.at}
          onClose={(refocus) => {
            setMenu(null);
            if (refocus) moreRef.current?.focus();
          }}
          onPick={(command) => {
            setMenu(null);
            if (command === "rename") startRename();
            else onCommand(command);
          }}
        />
      )}
    </div>
  );
}
