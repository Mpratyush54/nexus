import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useReducer,
  useRef,
  useState,
} from "react";
import {
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { Link, useSearchParams } from "react-router-dom";
import {
  ArrowDown,
  ArrowUp,
  ChevronRight,
  GitFork,
  Monitor,
  MoreHorizontal,
  Paperclip,
  Plus,
  RefreshCw,
  Search,
  Share2,
  Square,
  X,
} from "lucide-react";
import { chatsApi, type ChatCard, type ChatSession, type ChatTurn } from "@/api/chats";
import {
  desktopApi,
  isFinished,
  watchDesktopJob,
  type DesktopJob,
} from "@/api/desktop";
import {
  ChatMessage,
  ChatWorkLog,
  isSilentTurn,
  isToolOnlyTurn,
  type MessageActions,
} from "@/components/ChatMessage";
import { ChatDiff } from "@/components/ChatDiff";
import { ProjectSwitcher } from "@/components/ProjectSwitcher";
import { NexusMark } from "@/components/NexusBrand";
import { AccessibleModal } from "@/components/ui/AccessibleModal";
import { ToastHost } from "@/components/Toasts";
import { showToast } from "@/lib/toasts";
import { useAuth } from "@/providers/AuthProvider";
import { readableChatText } from "@/lib/chat-content";
import { memoryTitle } from "@/lib/plain-language";
import { DESKTOP_RELAY } from "@/lib/env";
import {
  agentName,
  chatTitle,
  composerHarness,
  parseTurnText,
} from "@/lib/harness-message";
import { setLiveChat, useLiveChats } from "@/lib/live-chats";
import { formatRelative } from "@/utils/format";
import { useDraft, useDraftsVersion } from "@/lib/drafts";
import {
  enterAction,
  isPrintableKey,
  isTouchComposer,
  recallStep,
} from "@/lib/composer-keys";
import { editIntoComposer, outbox } from "@/lib/outbox";
import {
  forgetChat,
  groupChats,
  renameChat,
  setArchived,
  togglePin,
  useChatPrefs,
} from "@/lib/chat-prefs";
import { comboLabel, isMod, onAppCommand } from "@/lib/shortcuts";
import { ApiError } from "@/types/api";
import {
  MAX_IMAGES,
  attachBlocker,
  fileProblem,
  pastedImages,
  prepareImage,
  relayErrorMessage,
  type DraftImage,
} from "@/lib/image-attach";
import { ChatRow, type RowCommand } from "./chats/ChatList";
import { ReplyStatus } from "./chats/ReplyStatus";

const message = (error: unknown) =>
  error instanceof Error ? error.message : "Request failed. Try again.";
const HARNESSES = ["codex", "cursor", "opencode", "claude"] as const;
const reducedMotion = () =>
  typeof window !== "undefined" &&
  window.matchMedia?.("(prefers-reduced-motion: reduce)").matches;

type Segment =
  | { kind: "turn"; turn: ChatTurn }
  | { kind: "worklog"; turns: ChatTurn[] };

/** Drops harness-only turns and folds runs of tool-only agent turns into one work log. */
function segmentTurns(rows: ChatTurn[]): Segment[] {
  const out: Segment[] = [];
  for (const turn of rows) {
    if (isSilentTurn(turn)) continue;
    if (isToolOnlyTurn(turn)) {
      const last = out[out.length - 1];
      if (last?.kind === "worklog") last.turns.push(turn);
      else out.push({ kind: "worklog", turns: [turn] });
      continue;
    }
    out.push({ kind: "turn", turn });
  }
  return out;
}

/** The words a person typed in a user turn (without harness wrappers). */
function promptText(turn: ChatTurn | undefined): string {
  return turn ? parseTurnText(turn.text_preview ?? "").text.trim() : "";
}

/** True when focus is in something that takes typing. */
function isTypingTarget(target: EventTarget | null) {
  const el = target as HTMLElement | null;
  return !!el?.closest?.("input, textarea, select, [contenteditable=''], [contenteditable='true']");
}

export function ChatsPage() {
  const { projectId, setProjectId, user } = useAuth();
  const userId = user?.userId ?? "";
  const cache = useQueryClient();
  const [params, setParams] = useSearchParams();
  const id = params.get("chat") || "";
  // Home's search box links here with ?q=.
  const [search, setSearch] = useState(() => params.get("q") ?? ""),
    [query, setQuery] = useState(() => params.get("q") ?? "");
  // Ctrl K "Search chats for …" can land here while the page is already open.
  const urlQ = params.get("q");
  const [seenQ, setSeenQ] = useState(urlQ);
  if (urlQ !== seenQ) {
    setSeenQ(urlQ);
    if (urlQ !== null) {
      setQuery(urlQ);
      setSearch(urlQ);
    }
  }
  const feed = useInfiniteQuery({
    queryKey: ["chats", projectId, search],
    enabled: !!projectId,
    initialPageParam: "",
    queryFn: ({ pageParam, signal }) =>
      chatsApi.list(projectId!, search, pageParam, signal),
    getNextPageParam: (last) => last.next_cursor || undefined,
  });
  const prefs = useChatPrefs(userId);
  useDraftsVersion();
  // Chats deleted a moment ago: hidden while their Undo toast is up.
  const [deleting, setDeleting] = useState<Set<string>>(() => new Set());
  const [confirmDelete, setConfirmDelete] = useState<ChatCard | null>(null);
  const [showArchived, setShowArchived] = useState(false);
  const allCards = (feed.data?.pages.flatMap((page) => page.items) ?? []).filter(
    (c) => !deleting.has(c.session_id),
  );
  const live = useLiveChats();
  // Filter by agent when the list mixes several (client-side, over loaded chats).
  const [agentFilter, setAgentFilter] = useState("");
  const agentsInList = [...new Set(allCards.map((c) => agentName(c.harness)))];
  const cards = agentFilter
    ? allCards.filter((c) => agentName(c.harness) === agentFilter)
    : allCards;
  const groups = groupChats(cards, prefs);
  const session = useQuery({
    queryKey: ["chat", id],
    enabled: !!id,
    queryFn: ({ signal }) => chatsApi.get(id, signal),
  });
  const create = useMutation({
    mutationFn: () => chatsApi.create(projectId!),
    onSuccess: (chat) => {
      setQuery("");
      setSearch("");
      setParams({ chat: chat.id });
      void cache.invalidateQueries({ queryKey: ["chats"] });
    },
  });
  // "New chat" from the command palette lands here as ?new=1.
  const wantsNew = params.get("new") === "1";
  useEffect(() => {
    if (!wantsNew) return;
    setParams(
      (current) => {
        const next = new URLSearchParams(current);
        next.delete("new");
        return next;
      },
      { replace: true },
    );
    if (DESKTOP_RELAY && projectId && !create.isPending) create.mutate();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [wantsNew, projectId]);
  useEffect(
    () =>
      onAppCommand((command) => {
        if (command === "new-chat" && DESKTOP_RELAY && projectId) create.mutate();
      }),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [projectId],
  );
  useEffect(() => {
    const timer = setTimeout(() => setSearch(query), 250);
    return () => clearTimeout(timer);
  }, [query]);

  function deleteSoon(card: ChatCard) {
    const chatId = card.session_id;
    setDeleting((s) => new Set(s).add(chatId));
    if (chatId === id) setParams({});
    let undone = false;
    const restore = () => {
      undone = true;
      setDeleting((s) => {
        const next = new Set(s);
        next.delete(chatId);
        return next;
      });
    };
    // Deleting is permanent, so it waits out the Undo window first.
    showToast("Chat deleted", { undo: restore });
    window.setTimeout(() => {
      if (undone) return;
      chatsApi
        .remove(chatId)
        .then(() => {
          forgetChat(userId, chatId);
          void cache.invalidateQueries({ queryKey: ["chats"] });
        })
        .catch((e) => {
          restore();
          showToast(
            e instanceof ApiError && e.status === 403
              ? "Only the person who owns this chat can delete it."
              : `Couldn't delete the chat. ${message(e)}`,
            { tone: "error" },
          );
        });
    }, 8000);
  }

  function runRowCommand(card: ChatCard, command: RowCommand) {
    const chatId = card.session_id;
    if (command === "pin") {
      const nowPinned = togglePin(userId, chatId);
      showToast(nowPinned ? "Pinned to the top" : "Unpinned");
    } else if (command === "archive") {
      const archived = prefs.archived.includes(chatId);
      setArchived(userId, chatId, !archived);
      showToast(archived ? "Chat moved back to your list" : "Chat archived", {
        undo: () => setArchived(userId, chatId, archived),
      });
    } else if (command === "link") {
      const url = new URL(window.location.href);
      url.search = new URLSearchParams({ chat: chatId }).toString();
      url.hash = "";
      void navigator.clipboard
        .writeText(url.toString())
        .then(() => showToast("Link copied. It opens for people who can see this chat."))
        .catch(() => showToast("Copy was blocked by the browser.", { tone: "error" }));
    } else if (command === "delete") {
      setConfirmDelete(card);
    }
  }

  const row = (card: ChatCard) => (
    <ChatRow
      key={card.session_id}
      card={card}
      title={prefs.titles[card.session_id]}
      active={id === card.session_id}
      pinned={prefs.pinned.includes(card.session_id)}
      archived={prefs.archived.includes(card.session_id)}
      live={live.get(card.session_id)}
      onOpen={() => setParams({ chat: card.session_id })}
      onCommand={(command) => runRowCommand(card, command)}
      onRename={(title) => {
        const original = chatTitle(card.title, card.summary, card.harness);
        renameChat(userId, card.session_id, title.trim() === original ? "" : title);
      }}
    />
  );

  const inProject = session.data?.project_id === projectId;
  return (
    <div className={`conversation-layout ${id ? "has-chat" : ""}`}>
      <aside className="conversation-list" aria-label="Project chats">
        <header>
          <h1>Chats</h1>
          <button
            title="Refresh chats"
            aria-label="Refresh chats"
            disabled={feed.isFetching}
            onClick={() => {
              void feed.refetch();
            }}
          >
            <RefreshCw size={15} className={feed.isFetching ? "animate-spin" : ""} />
          </button>
        </header>
        {/* The sidebar's project picker is hidden on narrow screens; this one stands in there. */}
        <div className="chat-project-switch">
          <ProjectSwitcher compact />
        </div>
        {DESKTOP_RELAY && (
          <button
            className="chat-new"
            disabled={!projectId || create.isPending}
            onClick={() => create.mutate()}
          >
            <Plus size={15} />
            {create.isPending ? "Creating…" : "New conversation"}
          </button>
        )}
        {create.error && (
          <p role="alert" className="chat-error">
            {message(create.error)}
          </p>
        )}
        <label className="conversation-search">
          <Search size={15} />
          <input
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder="Search chats and messages"
            aria-label="Search project chats and their messages"
          />
          {query && (
            <button aria-label="Clear search" onClick={() => setQuery("")}>
              <X size={14} />
            </button>
          )}
        </label>
        {agentsInList.length > 1 && (
          <label className="conversation-filters">
            <span>Agent</span>
            <select
              value={agentFilter}
              onChange={(e) => setAgentFilter(e.target.value)}
              aria-label="Filter by agent"
            >
              <option value="">All agents</option>
              {agentsInList.map((name) => (
                <option key={name} value={name}>
                  {name}
                </option>
              ))}
            </select>
          </label>
        )}
        <div className="conversation-cards">
          {feed.isPending && (
            <p className="chat-muted" role="status">Loading chats…</p>
          )}
          {feed.error && (
            <p role="alert" className="chat-error">
              {message(feed.error)}
            </p>
          )}
          {!feed.isPending && !feed.error && !cards.length && (
            <p className="chat-muted">
              {query
                ? `No chats or messages match “${query.trim()}”.`
                : agentFilter
                  ? `No ${agentFilter} chats loaded. Pick “All agents”.`
                  : "No chats yet. Install the desktop app and chat with Claude Code, Codex or Cursor in this project — your chats show up here."}
            </p>
          )}
          {groups.pinned.length > 0 && (
            <section aria-label="Pinned chats" className="conversation-group">
              <h2>Pinned</h2>
              {groups.pinned.map(row)}
            </section>
          )}
          {groups.recent.length > 0 && groups.pinned.length > 0 && (
            <h2 className="conversation-group-title">Recent</h2>
          )}
          {groups.recent.map(row)}
          {feed.hasNextPage && (
            <button
              className="chat-load-more"
              disabled={feed.isFetchingNextPage}
              onClick={() => {
                void feed.fetchNextPage();
              }}
            >
              {feed.isFetchingNextPage ? "Loading…" : "Load more chats"}
            </button>
          )}
          {groups.archived.length > 0 && (
            <section aria-label="Archived chats" className="conversation-group is-archived">
              <button
                type="button"
                className="conversation-archived-toggle"
                aria-expanded={showArchived}
                onClick={() => setShowArchived(!showArchived)}
              >
                <ChevronRight size={13} className="chat-chevron" aria-hidden />
                Archived ({groups.archived.length})
              </button>
              {showArchived && groups.archived.map(row)}
            </section>
          )}
        </div>
      </aside>
      {!id ? (
        <section className="conversation-welcome">
          <NexusMark size={44} />
          <h2>Pick up where the work left off.</h2>
          <p>
            Every chat you have with an AI coding agent is saved here. Pick one
            to read it and see which files it changed.
          </p>
          {!feed.isPending && !cards.length && !query && (
            <Link to="/app/connect">
              Set up Nexus Desktop <Monitor size={16} />
            </Link>
          )}
        </section>
      ) : session.isPending ? (
        <div className="conversation-detail conversation-skeleton" role="status" aria-label="Loading chat">
          <span />
          <span />
          <span />
        </div>
      ) : session.error ? (
        session.error instanceof ApiError &&
        (session.error.status === 404 || session.error.status === 403) ? (
          <section className="conversation-welcome" role="status">
            <NexusMark size={44} />
            <h2>We couldn’t find this chat.</h2>
            <p>
              It may have been deleted, or it belongs to someone who hasn’t shared it with you.
              Your other chats are in the list.
            </p>
            <button className="chat-welcome-action" onClick={() => setParams({})}>
              Back to all chats
            </button>
          </section>
        ) : (
          <div role="alert" className="chat-error conversation-state">
            <p>{`Couldn't open this chat. ${message(session.error)}`}</p>
            <button onClick={() => setParams({})}>Back to chats</button>
          </div>
        )
      ) : !inProject ? (
        <div role="alert" className="chat-error conversation-state">
          <p>This chat is in another project.</p>
          <div className="flex flex-wrap gap-2">
            {session.data?.project_id && (
              <button onClick={() => setProjectId(session.data.project_id)}>
                Switch to that project
              </button>
            )}
            <button onClick={() => setParams({})}>Back to chats</button>
          </div>
        </div>
      ) : (
        session.data && (
          <Conversation
            key={`${projectId}:${id}`}
            session={session.data}
            title={prefs.titles[id]}
            onBack={() => setParams({})}
            onOpen={(next) => setParams({ chat: next })}
          />
        )
      )}
      {confirmDelete && (
        <AccessibleModal labelledBy="delete-chat-title" onClose={() => setConfirmDelete(null)}>
          <header>
            <h3 id="delete-chat-title">Delete this chat?</h3>
            <button aria-label="Cancel" onClick={() => setConfirmDelete(null)}>
              <X size={18} />
            </button>
          </header>
          <p>
            “{prefs.titles[confirmDelete.session_id] || chatTitle(confirmDelete.title, confirmDelete.summary, confirmDelete.harness)}”
            and its messages are removed for everyone it’s shared with. Memories your agents saved from it stay.
          </p>
          <div className="chat-modal-actions">
            <button onClick={() => setConfirmDelete(null)}>Cancel</button>
            <button
              className="chat-danger"
              autoFocus
              onClick={() => {
                deleteSoon(confirmDelete);
                setConfirmDelete(null);
              }}
            >
              Delete chat
            </button>
          </div>
        </AccessibleModal>
      )}
      <ToastHost />
    </div>
  );
}

/**
 * A web reply runs on your computer only after you approve it there (so a
 * stolen session can't run commands). While it waits, say so calmly and keep
 * the clock moving so it never looks frozen.
 */
function ApprovalWait({ computer }: { computer: string }) {
  const [start] = useState(() => Date.now());
  const [now, setNow] = useState(start);
  useEffect(() => {
    const t = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(t);
  }, []);
  const secs = Math.max(0, Math.round((now - start) / 1000));
  const waited = secs < 60 ? `${secs} s` : `${Math.floor(secs / 60)} min ${secs % 60} s`;
  return (
    <div className="chat-approval" role="status">
      <span className="chat-approval-pulse" aria-hidden="true" />
      <div>
        <p className="chat-approval-title">Waiting for approval on {computer}</p>
        <p className="chat-approval-help">
          Open Nexus on that computer and select Approve. Replies sent from the web always need this
          step, so nobody else can run your agent.{secs > 0 ? ` Waiting ${waited}.` : ""}
        </p>
      </div>
    </div>
  );
}

function Conversation({
  session,
  title,
  onBack,
  onOpen,
}: {
  session: ChatSession;
  title?: string;
  onBack: () => void;
  onOpen: (id: string) => void;
}) {
  const { user } = useAuth();
  const owned = !!user?.userId && session.owner_user_id === user.userId;
  const cache = useQueryClient();
  const [touch] = useState(isTouchComposer);
  const [tab, setTab] = useState("chat"),
    [findOpen, setFindOpen] = useState(false),
    [find, setFind] = useState(""),
    [findQuery, setFindQuery] = useState(""),
    [matchIndex, setMatchIndex] = useState(0);
  const [shareOpen, setShareOpen] = useState(false),
    [shareUser, setShareUser] = useState("");
  const [agent, setAgent] = useState(composerHarness(session.harness)),
    [model, setModel] = useState(""),
    [deviceId, setDeviceId] = useState("");
  // What you're typing is kept per chat, across switches and reloads.
  const [draft, setDraft, clearDraft] = useDraft(session.id);
  const [ownJob, setJob] = useState<DesktopJob | null>(null),
    [error, setError] = useState(""),
    [streamNonce, setStreamNonce] = useState(0);
  const [modelJob, setModelJob] = useState<DesktopJob | null>(null);
  // Replies sent from here that finished but haven't synced into the transcript yet.
  const [earlierJobs, setEarlierJobs] = useState<DesktopJob[]>([]);
  // The message just sent, shown before the server answers (and kept if it fails).
  const [outgoing, dispatchOut] = useReducer(outbox, null);
  const [startedAt, setStartedAt] = useState(() => Date.now());
  // Images waiting in the composer, and the ones shown under the message
  // just sent (object URLs; the bytes leave this tab only in the send).
  const [images, setImages] = useState<DraftImage[]>([]);
  const [sentImages, setSentImages] = useState<DraftImage[]>([]);
  const [readingImages, setReadingImages] = useState(0);
  // Image problems are said next to the 📎, not at the top of the chat.
  const [attachNote, setAttachNote] = useState("");
  const fileInput = useRef<HTMLInputElement>(null);
  const composer = useRef<HTMLTextAreaElement>(null);
  const objectUrls = useRef(new Set<string>());
  useEffect(() => {
    const urls = objectUrls.current;
    return () => urls.forEach((url) => URL.revokeObjectURL(url));
  }, []);
  const sendingLock = useRef(false);
  const sentPrompt = useRef("");
  const [requestParams, setRequestParams] = useSearchParams();
  const savedRequest = requestParams.get("request") || "";
  const restoredJob = useQuery({
    queryKey: ["desktop-job", savedRequest],
    enabled: DESKTOP_RELAY && !!savedRequest && !ownJob,
    queryFn: ({ signal }) => desktopApi.get(savedRequest, signal),
    retry: 1,
  });
  const job =
    ownJob ??
    (restoredJob.data?.session_id === session.id ? restoredJob.data : null);
  const scroll = useRef<HTMLDivElement>(null);
  const transcript = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const vv = window.visualViewport;
    if (!vv) return;
    const root = document.documentElement;
    const sync = () => root.style.setProperty("--nx-vvh", `${vv.height}px`);
    sync();
    vv.addEventListener("resize", sync);
    return () => {
      vv.removeEventListener("resize", sync);
      root.style.removeProperty("--nx-vvh");
    };
  }, []);
  const scrolledInitially = useRef(false);
  const followReply = useRef(true);
  const [awayFromLatest, setAwayFromLatest] = useState(false);
  const turns = useInfiniteQuery({
    queryKey: ["chat-turns", session.id, findQuery],
    initialPageParam: "",
    queryFn: ({ pageParam, signal }) =>
      chatsApi.turns(session.id, pageParam, findQuery, signal),
    getNextPageParam: (last) => last.next_cursor || undefined,
  });
  const summary = useQuery({
    queryKey: ["chat-summary", session.id],
    enabled: tab === "memory",
    queryFn: ({ signal }) => chatsApi.summary(session.id, signal),
  });
  const memory = useQuery({
    queryKey: ["chat-memory", session.id],
    enabled: tab === "memory",
    queryFn: ({ signal }) => chatsApi.memory(session.id, signal),
  });
  const devices = useQuery({
    queryKey: ["desktop-devices"],
    enabled: DESKTOP_RELAY,
    queryFn: ({ signal }) => desktopApi.devices(signal),
    refetchInterval: 15000,
    retry: 1,
  });
  const available =
    devices.data?.items.filter(
      (d) =>
        d.projects?.includes(session.project_id) &&
        Date.now() - new Date(d.last_seen_at).getTime() < 45000,
    ) ?? [];
  const device = available.find((d) => d.id === deviceId) ?? available[0];
  const sending = outgoing?.state === "sending";
  const running = !!job && !isFinished(job.state);
  const busy = sending || running;
  const imageBlocker = device ? attachBlocker(agent, device.protocol) : "";
  // Reply from here only when it can actually run: relay on, your chat, a computer online for it.
  const steerable = DESKTOP_RELAY && owned && !!device;
  const knownComputer = devices.data?.items.find((d) => d.projects?.includes(session.project_id));
  const replyNote = !owned
    ? "Shared with you. Use “Continue as copy” to reply in your own chat."
    : `Open Nexus on ${knownComputer?.name ?? "your computer"} to reply from here.`;
  const discoveringModels = !!modelJob && !isFinished(modelJob.state);
  const modelLock = useRef(false);
  const recipients = useQuery({
    queryKey: ["share-recipients", session.project_id],
    enabled: shareOpen,
    queryFn: () => chatsApi.recipients(session.project_id),
  });
  const recipientList = recipients.data?.people ?? recipients.data?.items ?? [];
  const share = useMutation({
    mutationFn: () => chatsApi.share(session.id, shareUser),
    onSuccess: () => {
      setShareOpen(false);
      setShareUser("");
    },
  });
  const fork = useMutation({
    mutationFn: () => chatsApi.fork(session.id),
    onSuccess: (child) => {
      void cache.invalidateQueries({ queryKey: ["chats"] });
      onOpen(child.id);
    },
  });
  const pages = turns.data?.pages ?? [];
  const rows = (findQuery ? pages : pages.toReversed()).flatMap(
    (page) => page.items,
  );
  const segments = segmentTurns(rows);
  const prompts = rows.filter(
    (turn) => turn.role === "user" && readableChatText(turn.text_preview || ""),
  );

  // ---- Focus: opening a chat puts the caret in the composer (not on phones,
  // where that would throw the keyboard over the conversation).
  const focusComposer = useCallback(() => {
    const el = composer.current;
    if (!el) return;
    el.focus({ preventScroll: true });
    el.setSelectionRange(el.value.length, el.value.length);
  }, []);
  const focusedOnOpen = useRef(false);
  useEffect(() => {
    if (focusedOnOpen.current || !composer.current || touch) return;
    focusedOnOpen.current = true;
    focusComposer();
  }, [steerable, busy, touch, focusComposer]);

  // ---- Composer grows with its text, up to a limit, instead of a resize handle.
  useLayoutEffect(() => {
    const el = composer.current;
    if (!el) return;
    el.style.height = "auto";
    el.style.height = `${Math.min(el.scrollHeight, touch ? 160 : 240)}px`;
  }, [draft, touch, steerable]);

  // ---- Scroll: start at the latest message, and stay pinned to it while a
  // reply streams in, unless the reader scrolled up.
  useEffect(() => {
    if (
      !findQuery &&
      !scrolledInitially.current &&
      turns.data &&
      scroll.current
    ) {
      scroll.current.scrollTop = scroll.current.scrollHeight;
      scrolledInitially.current = true;
    }
  }, [turns.data, findQuery]);
  useEffect(() => {
    const node = transcript.current;
    if (!node || typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(() => {
      const box = scroll.current;
      if (box && followReply.current && scrolledInitially.current && !anchor.current)
        box.scrollTop = box.scrollHeight;
    });
    observer.observe(node);
    return () => observer.disconnect();
  }, [tab]);
  // Loading earlier messages prepends rows: keep the reader's place.
  const anchor = useRef<{ height: number; top: number } | null>(null);
  useEffect(() => {
    const node = scroll.current;
    if (!node || !anchor.current) return;
    node.scrollTop = anchor.current.top + (node.scrollHeight - anchor.current.height);
    anchor.current = null;
  }, [pages.length]);
  function loadEarlier() {
    const node = scroll.current;
    if (node) anchor.current = { height: node.scrollHeight, top: node.scrollTop };
    void turns.fetchNextPage();
  }
  function jumpToLatest() {
    const node = scroll.current;
    if (!node) return;
    followReply.current = true;
    node.scrollTo({
      top: node.scrollHeight,
      behavior: reducedMotion() ? "auto" : "smooth",
    });
  }
  useEffect(() => {
    const timer = setTimeout(() => {
      setFindQuery(find);
      setMatchIndex(0);
    }, 250);
    return () => clearTimeout(timer);
  }, [find]);
  useEffect(() => {
    if (!job || isFinished(job.state)) return;
    const controller = new AbortController();
    void watchDesktopJob(job.id, controller.signal, (next) => {
      setJob(next);
      setLiveChat(next.session_id, next.state);
      setRequestParams(
        (current) => {
          const nextParams = new URLSearchParams(current);
          nextParams.set("request", next.id);
          return nextParams;
        },
        { replace: true },
      );
      if (isFinished(next.state)) {
        void cache.invalidateQueries({ queryKey: ["chat-turns", session.id] });
        void cache.invalidateQueries({ queryKey: ["chats"] });
      }
    }).catch((e) => {
      if (!controller.signal.aborted) setError(message(e));
    });
    return () => controller.abort();
    // A stream belongs to one immutable request, not the currently selected harness.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [job?.id, streamNonce, cache, session.id]);
  useEffect(() => {
    if (!modelJob || isFinished(modelJob.state)) return;
    const controller = new AbortController();
    void watchDesktopJob(modelJob.id, controller.signal, setModelJob).catch(
      (e) => {
        if (!controller.signal.aborted) setError(message(e));
      },
    );
    return () => controller.abort();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [modelJob?.id]);

  /**
   * Sends a message. The bubble appears at once and the composer empties;
   * the request goes out after. If it fails, the bubble stays with Retry and Edit.
   */
  async function send(opts: { text?: string; images?: DraftImage[]; retryId?: string } = {}) {
    const fromComposer = opts.text === undefined;
    const text = (opts.text ?? draft).trim();
    const pics = opts.images ?? (fromComposer ? images : []);
    if (
      !owned ||
      !device ||
      busy ||
      discoveringModels ||
      modelLock.current ||
      sendingLock.current ||
      !text ||
      (fromComposer && readingImages > 0)
    )
      return;
    if (pics.length > 0 && imageBlocker) {
      setAttachNote(imageBlocker);
      return;
    }
    const outId = opts.retryId ?? crypto.randomUUID();
    sendingLock.current = true;
    dispatchOut(
      opts.retryId
        ? { type: "retry", id: outId }
        : { type: "send", id: outId, text, images: pics },
    );
    if (fromComposer) {
      clearDraft();
      setImages([]);
      setAttachNote("");
    }
    setRecall(-1);
    setError("");
    followReply.current = true;
    requestAnimationFrame(() => jumpToLatest());
    try {
      const next = await desktopApi.create({
        id: crypto.randomUUID(),
        device_id: device.id,
        session_id: session.id,
        project_id: session.project_id,
        kind: "turn",
        agent,
        model,
        prompt: text,
        ...(pics.length > 0 && {
          attachments: pics.map((image) => ({
            name: image.name,
            media_type: image.mediaType,
            data: image.data,
          })),
        }),
      });
      if (job && !isSynced(job)) setEarlierJobs((list) => [...list, job]);
      sentPrompt.current = text;
      setStartedAt(Date.now());
      setLiveChat(session.id, next.state);
      setSentImages(pics);
      setJob(next);
      dispatchOut({ type: "sent", id: outId });
    } catch (e) {
      dispatchOut({ type: "failed", id: outId, error: relayErrorMessage(message(e)) });
    } finally {
      sendingLock.current = false;
    }
  }
  function editFailed() {
    if (!outgoing) return;
    setDraft(editIntoComposer(outgoing.text, draft));
    if (!images.length && outgoing.images.length) setImages(outgoing.images);
    dispatchOut({ type: "discard", id: outgoing.id });
    requestAnimationFrame(focusComposer);
  }
  function stop() {
    if (!job || isFinished(job.state)) return;
    void desktopApi
      .cancel(job.id)
      .then((next) => {
        // Keep what already ran: a stopped turn still shows its finished steps.
        setJob((prev) =>
          prev && !next.activities?.length && prev.activities?.length
            ? {
                ...next,
                text: next.text || prev.text,
                activities: prev.activities.filter(
                  (a) => a.status !== "running",
                ),
              }
            : next,
        );
        setLiveChat(next.session_id, next.state);
      })
      .catch((e) => setError(message(e)));
  }
  /** Puts an earlier message in the composer to change and send again. */
  function editMessage(text: string) {
    setDraft(editIntoComposer(text, draft));
    requestAnimationFrame(focusComposer);
  }

  // ---- ↑ in an empty composer recalls your earlier prompts, newest first.
  const history = [
    ...new Set(
      [...prompts.map((t) => promptText(t)), job?.prompt ?? ""]
        .filter(Boolean)
        .reverse(),
    ),
  ];
  const [recall, setRecall] = useState(-1);

  // ---- Keys that work anywhere in the open chat.
  const keyState = useRef({ busy, stop, jumpToLatest, focusComposer, tab });
  keyState.current = { busy, stop, jumpToLatest, focusComposer, tab };
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const k = keyState.current;
      if (e.defaultPrevented) return;
      const modalOpen = !!document.querySelector("dialog[open], [role=menu]");
      // Esc stops from the composer or while reading; other text boxes keep their own Esc.
      if (
        e.key === "Escape" &&
        k.busy &&
        !modalOpen &&
        (!isTypingTarget(e.target) || e.target === composer.current)
      ) {
        e.preventDefault();
        k.stop();
        return;
      }
      if (isMod(e) && e.shiftKey && e.key === "Backspace" && k.busy) {
        e.preventDefault();
        k.stop();
        return;
      }
      if (isMod(e) && e.key === "ArrowDown" && !isTypingTarget(e.target)) {
        e.preventDefault();
        k.jumpToLatest();
        return;
      }
      if (isMod(e) && !e.shiftKey && e.key.toLowerCase() === "f" && k.tab !== "memory") {
        e.preventDefault();
        setFindOpen(true);
        requestAnimationFrame(() =>
          document.querySelector<HTMLInputElement>(".conversation-find input")?.focus(),
        );
        return;
      }
      // Typing while reading the transcript goes into the composer.
      if (
        isPrintableKey(e) &&
        !modalOpen &&
        !isTypingTarget(e.target) &&
        !(e.target as HTMLElement | null)?.closest?.(
          ".conversation-list, [role=tablist], button, a, summary, [role=menu]",
        ) &&
        composer.current
      ) {
        composer.current.focus({ preventScroll: true });
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);
  useEffect(
    () =>
      onAppCommand((command) => {
        if (command === "stop-reply") keyState.current.stop();
        if (command === "focus-composer") keyState.current.focusComposer();
        if (command === "find-in-chat") {
          setTab("chat");
          setFindOpen(true);
        }
      }),
    [],
  );

  async function addImages(files: File[]) {
    if (!files.length) return;
    if (imageBlocker) {
      setAttachNote(imageBlocker);
      return;
    }
    setAttachNote("");
    let count = images.length;
    for (const file of files) {
      const problem = fileProblem(file, count);
      if (problem) {
        setAttachNote(problem);
        continue;
      }
      count++;
      setReadingImages((n) => n + 1);
      try {
        const image = await prepareImage(file);
        objectUrls.current.add(image.url);
        setImages((current) =>
          current.length >= MAX_IMAGES ? current : [...current, image],
        );
      } catch (e) {
        setAttachNote(message(e));
      } finally {
        setReadingImages((n) => n - 1);
      }
    }
  }
  function removeImage(id: string) {
    setImages((current) => {
      const gone = current.find((image) => image.id === id);
      if (gone) {
        URL.revokeObjectURL(gone.url);
        objectUrls.current.delete(gone.url);
      }
      return current.filter((image) => image.id !== id);
    });
  }
  async function models() {
    if (
      !device ||
      busy ||
      discoveringModels ||
      modelLock.current ||
      sendingLock.current
    )
      return;
    modelLock.current = true;
    setError("");
    try {
      setModelJob(
        await desktopApi.create({
          id: crypto.randomUUID(),
          device_id: device.id,
          session_id: "",
          project_id: session.project_id,
          kind: "models",
          agent,
        }),
      );
    } catch (e) {
      setError(message(e));
    } finally {
      modelLock.current = false;
    }
  }
  function jump(turn: ChatTurn) {
    document
      .getElementById(`turn-${turn.cursor_idx || turn.idx}-${turn.role}`)
      ?.scrollIntoView({
        behavior: reducedMotion() ? "instant" : "smooth",
        block: "start",
      });
  }
  function nextMatch(direction: number) {
    if (!rows.length) return;
    const next = (matchIndex + direction + rows.length) % rows.length;
    setMatchIndex(next);
    jump(rows[next]);
  }
  const files = job?.result?.file_diffs ?? [];
  function isSynced(j: DesktopJob) {
    return (
      !!j.result?.answer_cursor &&
      rows.some((turn) => turn.role === "assistant" && turn.cursor_idx === j.result?.answer_cursor)
    );
  }
  const responseAlreadySynced = !!job && isSynced(job);
  const jobComputer =
    devices.data?.items.find((d) => d.id === job?.device_id)?.name ||
    device?.name ||
    "your computer";
  const showJob = !!job && !responseAlreadySynced;
  const canReply = steerable && !busy;

  // The latest reply keeps its actions visible; Retry asks its prompt again.
  const lastSegment = segments[segments.length - 1];
  const lastIsReply =
    !showJob &&
    !outgoing &&
    lastSegment?.kind === "turn" &&
    lastSegment.turn.role !== "user";
  const lastPrompt = promptText(prompts[prompts.length - 1]);
  const replyActions = (isLatest: boolean, prompt: string): MessageActions => ({
    always: isLatest,
    onRetry: isLatest && canReply && prompt ? () => void send({ text: prompt }) : undefined,
    onFork: isLatest && owned && !busy ? () => fork.mutate() : undefined,
  });
  const userActions = (text: string): MessageActions | undefined =>
    steerable && text ? { onEdit: () => editMessage(text) } : undefined;

  const statusVerb = !job
    ? ""
    : job.state === "queued"
      ? `Sending to ${jobComputer}`
      : job.status || "Working";
  const liveStatus =
    job && !isFinished(job.state) ? (
      job.state === "awaiting_approval" ? (
        <ApprovalWait key={job.id} computer={jobComputer} />
      ) : (
        <ReplyStatus verb={statusVerb} startedAt={startedAt} touch={touch} />
      )
    ) : undefined;

  return (
    <section className="conversation-detail" aria-label="Conversation">
      <header className="conversation-header">
        <div>
          <button className="chat-mobile-back" onClick={onBack}>
            ← Chats
          </button>
          <h2>{title || chatTitle(session.title, session.summary, session.harness)}</h2>
          <small>
            {agentName(session.harness)}
            {session.last_active_at &&
              ` · ${formatRelative(session.last_active_at)}`}
          </small>
        </div>
        <div className="conversation-actions">
          {session.parent_session_id && (
            <button
              title="Open parent chat"
              aria-label="Open parent chat"
              onClick={() => onOpen(session.parent_session_id!)}
            >
              <GitFork size={16} />
            </button>
          )}
          <button
            title={`Find in chat (${comboLabel(["Mod", "F"])})`}
            aria-label="Find in chat"
            onClick={() => setFindOpen(!findOpen)}
          >
            <Search size={17} />
          </button>
          <button
            disabled={fork.isPending || busy}
            title="Copy this chat into a new one you can continue"
            onClick={() => fork.mutate()}
          >
            <GitFork size={15} />
            <span className="conversation-action-label">
              {fork.isPending ? "Copying…" : "Continue as copy"}
            </span>
          </button>
          <button
            disabled={!owned}
            title={
              owned ? "Share conversation" : "Only the owner can manage sharing"
            }
            aria-label="Share chat"
            onClick={() => setShareOpen(true)}
          >
            <Share2 size={15} />
            <span className="conversation-action-label">Share</span>
          </button>
        </div>
      </header>
      <div className="conversation-tabs" role="tablist" aria-label="Chat views">
        {[
          ["chat", "Chat"],
          ["activity", "Steps"],
          ["memory", "What it learned"],
        ].map(([name, label]) => (
          <button
            role="tab"
            id={`chat-tab-${name}`}
            aria-controls="chat-tabpanel"
            aria-selected={tab === name}
            tabIndex={tab === name ? 0 : -1}
            key={name}
            onClick={() => setTab(name)}
            onKeyDown={(e) => {
              const order = ["chat", "activity", "memory"];
              const at = order.indexOf(tab);
              if (e.key === "ArrowRight" || e.key === "ArrowLeft") {
                e.preventDefault();
                const next = order[(at + (e.key === "ArrowRight" ? 1 : order.length - 1)) % order.length];
                setTab(next);
                document.getElementById(`chat-tab-${next}`)?.focus();
              }
            }}
          >
            {label}
          </button>
        ))}
      </div>
      {findOpen && (
        <div className="conversation-find">
          <Search size={15} />
          <input
            autoFocus
            value={find}
            onChange={(e) => setFind(e.target.value)}
            placeholder="Find across this conversation"
            aria-label="Find across this conversation"
            onKeyDown={(e) => {
              if (e.key === "Enter") {
                e.preventDefault();
                nextMatch(e.shiftKey ? -1 : 1);
              } else if (e.key === "Escape") {
                e.preventDefault();
                setFindOpen(false);
                setFind("");
                focusComposer();
              }
            }}
          />
          <small>
            {findQuery
              ? `${rows.length}${turns.hasNextPage ? "+" : ""} matching turns`
              : ""}
          </small>
          <button aria-label="Previous match" onClick={() => nextMatch(-1)}>
            <ArrowUp size={15} />
          </button>
          <button aria-label="Next match" onClick={() => nextMatch(1)}>
            <ArrowDown size={15} />
          </button>
          <button
            aria-label="Close find"
            onClick={() => {
              setFindOpen(false);
              setFind("");
            }}
          >
            <X size={15} />
          </button>
        </div>
      )}
      <div className="conversation-stage">
      <div
        className="conversation-scroll"
        id="chat-tabpanel"
        role="tabpanel"
        aria-labelledby={`chat-tab-${tab}`}
        aria-live="off"
        tabIndex={0}
        ref={scroll}
        onScroll={() => {
          const node = scroll.current;
          if (!node) return;
          const distance = node.scrollHeight - node.scrollTop - node.clientHeight;
          followReply.current = distance < 80;
          setAwayFromLatest(distance > 200);
        }}
      >
        {(turns.error || error || fork.error) && (
          <p role="alert" className="chat-error">
            {error || message(turns.error || fork.error)}
            {busy && error && (
              <button
                onClick={() => {
                  setError("");
                  setStreamNonce((n) => n + 1);
                }}
              >
                Reconnect live view
              </button>
            )}
          </p>
        )}
        {turns.isPending && (
          <div className="conversation-skeleton is-inline" role="status" aria-label="Loading messages">
            <span />
            <span />
            <span />
          </div>
        )}
        {tab === "memory" ? (
          <div className="conversation-memory">
            <h3>Conversation summary</h3>
            <p>
              {summary.isPending
                ? "Loading summary…"
                : summary.error
                  ? message(summary.error)
                  : readableChatText(summary.data?.summary || session.summary || "") ||
                    "No summary yet. Nexus writes one after the chat has a few messages."}
            </p>
            <h3>Saved memories</h3>
            {memory.isPending ? (
              <p>Loading memories…</p>
            ) : memory.error ? (
              <p role="alert">{message(memory.error)}</p>
            ) : memory.data?.items.length ? (
              memory.data.items.map((item, i) => {
                const row = item as { content?: string; key?: string };
                const name = memoryTitle(row.key || "");
                return (
                  <article key={i}>
                    <strong title={row.key}>
                      {name.title}
                      {name.group && <small className="chat-memory-group"> · {name.group}</small>}
                    </strong>
                    <p>{row.content || "No readable content was saved."}</p>
                  </article>
                );
              })
            ) : (
              <p>No memories were saved from this chat.</p>
            )}
          </div>
        ) : (
          <div className="conversation-transcript" ref={transcript}>
            {!findQuery && turns.hasNextPage && (
              <button
                className="chat-load-more"
                disabled={turns.isFetchingNextPage}
                onClick={loadEarlier}
              >
                {turns.isFetchingNextPage
                  ? "Loading…"
                  : "Load earlier messages"}
              </button>
            )}
            {tab === "activity"
              ? rows.map((turn) => (
                  <ChatMessage
                    key={`${turn.cursor_idx || turn.idx}:${turn.role}`}
                    turn={{ ...turn, text_preview: "" }}
                    harness={session.harness}
                  />
                ))
              : segments.map((segment, index) =>
                  segment.kind === "worklog" ? (
                    <ChatWorkLog
                      key={`log:${segment.turns[0].cursor_idx || segment.turns[0].idx}`}
                      turns={segment.turns}
                      harness={session.harness}
                    />
                  ) : (
                    <ChatMessage
                      key={`${segment.turn.cursor_idx || segment.turn.idx}:${segment.turn.role}`}
                      turn={segment.turn}
                      harness={session.harness}
                      match={findQuery}
                      actions={
                        findQuery
                          ? undefined
                          : segment.turn.role === "user"
                            ? userActions(promptText(segment.turn))
                            : replyActions(lastIsReply && index === segments.length - 1, lastPrompt)
                      }
                    />
                  ),
                )}
            {!segments.length && !turns.isPending && !turns.error && !showJob && !outgoing && (
              <p className="chat-muted">
                {findQuery
                  ? `No messages match “${findQuery}”.`
                  : steerable
                    ? "No messages yet. Say what you'd like the agent to do."
                    : "No messages yet. They appear here once Nexus Desktop syncs this chat."}
              </p>
            )}
            {findQuery && turns.hasNextPage && (
              <button
                className="chat-load-more"
                disabled={turns.isFetchingNextPage}
                onClick={() => {
                  void turns.fetchNextPage();
                }}
              >
                {turns.isFetchingNextPage
                  ? "Loading…"
                  : findQuery
                    ? "Load more matches"
                    : "Load earlier messages"}
              </button>
            )}
            {tab === "chat" &&
              earlierJobs
                .filter((j) => j.id !== job?.id && !isSynced(j))
                .map((j) => (
                  <div key={`earlier-${j.id}`} className="chat-earlier-job">
                    <ChatMessage
                      turn={{ idx: -4, role: "user", text_preview: j.prompt || "" }}
                      harness={j.agent || agent}
                      actions={userActions(j.prompt || "")}
                    />
                    <ChatMessage
                      turn={{ idx: -4, role: "assistant", text_preview: j.result?.text || j.text, tool_calls: j.activities }}
                      harness={j.agent || agent}
                      toolsFirst
                    />
                  </div>
                ))}
            {tab === "chat" && showJob && job && (
              <>
                <ChatMessage
                  key={`job-user-${job.id}`}
                  turn={{ idx: -2, role: "user", text_preview: job.prompt || sentPrompt.current }}
                  harness={job.agent || agent}
                  images={
                    job.attachments?.length
                      ? sentImages.map((image) => ({ url: image.url, name: image.name }))
                      : []
                  }
                  actions={outgoing ? undefined : userActions(job.prompt || sentPrompt.current)}
                />
                <ChatMessage
                  key={`job-reply-${job.id}`}
                  turn={{
                    idx: -1,
                    role: "assistant",
                    text_preview: job.result?.text || job.text,
                    tool_calls: job.activities,
                  }}
                  harness={job.agent || agent}
                  isNew
                  toolsFirst
                  status={liveStatus}
                  actions={
                    !outgoing && isFinished(job.state)
                      ? replyActions(true, job.prompt || sentPrompt.current)
                      : undefined
                  }
                />
                {files.map((file) => (
                  <ChatDiff key={file.path} {...file} />
                ))}
                {job.error && (
                  <div role="alert" className="chat-error chat-reply-error">
                    <p>{job.error}</p>
                    {canReply && (job.prompt || sentPrompt.current) && (
                      <button type="button" onClick={() => void send({ text: job.prompt || sentPrompt.current })}>
                        Retry
                      </button>
                    )}
                  </div>
                )}
                {isFinished(job.state) && job.state !== "failed" && <JobOutcome job={job} canReply={canReply} onContinue={() => void send({ text: "continue" })} onRetry={() => void send({ text: job.prompt || sentPrompt.current })} />}
              </>
            )}
            {tab === "chat" && outgoing && (
              <ChatMessage
                key={`out-${outgoing.id}`}
                turn={{ idx: -3, role: "user", text_preview: outgoing.text }}
                harness={agent}
                isNew={outgoing.state === "sending"}
                images={outgoing.images.map((image) => ({ url: image.url, name: image.name }))}
                outgoing={{
                  state: outgoing.state,
                  error: outgoing.error,
                  onRetry: () =>
                    void send({ text: outgoing.text, images: outgoing.images, retryId: outgoing.id }),
                  onEdit: editFailed,
                }}
              />
            )}
          </div>
        )}
        {tab === "chat" && prompts.length > 1 && (
          <nav
            className="conversation-prompt-rail"
            aria-label="Jump to your prompts"
          >
            {prompts.map((turn, index) => (
              <button
                key={turn.cursor_idx || turn.idx}
                aria-label={`Jump to prompt ${index + 1}`}
                onClick={() => jump(turn)}
              >
                <span className="prompt-tick" />
                <span className="prompt-peek">
                  {readableChatText(turn.text_preview || "").slice(0, 200)}
                </span>
              </button>
            ))}
          </nav>
        )}
      </div>
        <button
          type="button"
          className={`chat-jump-latest ${awayFromLatest && tab === "chat" && !findQuery ? "is-visible" : ""}`}
          onClick={jumpToLatest}
          aria-label="Jump to latest message"
          title={`Jump to latest (${comboLabel(["Mod", "↓"])})`}
          tabIndex={awayFromLatest && tab === "chat" && !findQuery ? 0 : -1}
          aria-hidden={awayFromLatest && tab === "chat" && !findQuery ? undefined : true}
        >
          <ArrowDown size={16} />
          {busy && <span className="chat-jump-dot" aria-hidden="true" />}
        </button>
      </div>
      {steerable || busy ? (
      <form
        className="conversation-composer"
        aria-label="Reply"
        onSubmit={(e) => {
          e.preventDefault();
          if (busy) stop();
          else void send();
          if (!touch) requestAnimationFrame(focusComposer);
        }}
      >
        {attachNote && (
          <p className="composer-note" role="alert">
            {attachNote}
          </p>
        )}
        {images.length > 0 && (
          <ul className="composer-images" aria-label="Images to send">
            {images.map((image) => (
              <li key={image.id}>
                <img src={image.url} alt={image.name} />
                <button
                  type="button"
                  aria-label={"Remove " + image.name}
                  title={"Remove " + image.name}
                  disabled={sending}
                  onClick={() => removeImage(image.id)}
                >
                  <X size={12} />
                </button>
              </li>
            ))}
          </ul>
        )}
        <textarea
          ref={composer}
          rows={1}
          maxLength={16000}
          value={draft}
          enterKeyHint={touch ? "enter" : "send"}
          onChange={(e) => {
            setDraft(e.target.value);
            setRecall(-1);
          }}
          onPaste={(e) => {
            const files = pastedImages(e.clipboardData);
            if (!files.length) return;
            e.preventDefault();
            void addImages(files);
          }}
          placeholder={`Reply to ${agentName(agent)}…`}
          aria-label="Message the agent"
          aria-describedby="composer-hint"
          onKeyDown={(e) => {
            const action = enterAction(
              {
                key: e.key,
                shiftKey: e.shiftKey,
                ctrlKey: e.ctrlKey,
                metaKey: e.metaKey,
                altKey: e.altKey,
                isComposing: e.nativeEvent.isComposing,
                keyCode: e.keyCode,
              },
              touch,
            );
            if (action === "send") {
              e.preventDefault();
              if (!busy) void send();
              return;
            }
            if (e.key === "ArrowUp" || e.key === "ArrowDown") {
              const el = e.currentTarget;
              const atEdge =
                e.key === "ArrowUp"
                  ? el.selectionStart === 0 && el.selectionEnd === 0
                  : el.selectionStart === el.value.length;
              if (!atEdge && draft) return;
              const next = recallStep(e.key, recall, history.length, draft, history[recall]);
              if (next === null) return;
              e.preventDefault();
              setRecall(next);
              setDraft(next >= 0 ? history[next] : "");
              return;
            }
            if (e.key === "Escape" && !busy && recall >= 0) {
              e.preventDefault();
              setRecall(-1);
              setDraft("");
            }
          }}
        />
        <span id="composer-hint" className="sr-only">
          {touch
            ? "Use the Send button to send."
            : "Enter sends, Shift+Enter adds a line, up arrow recalls an earlier prompt."}
        </span>
        <div className="conversation-composer-footer">
          <div className="composer-tools">
            <input
              ref={fileInput}
              type="file"
              accept="image/png,image/jpeg,image/gif,image/webp"
              multiple
              hidden
              onChange={(e) => {
                const files = Array.from(e.target.files ?? []);
                e.target.value = "";
                void addImages(files);
              }}
            />
            <button
              type="button"
              className="composer-attach"
              aria-label="Attach images"
              title={imageBlocker || "Attach images (or paste them)"}
              aria-disabled={!!imageBlocker || busy || images.length >= MAX_IMAGES}
              onClick={() => {
                if (imageBlocker) setAttachNote(imageBlocker);
                else if (images.length >= MAX_IMAGES) setAttachNote("Attach at most " + MAX_IMAGES + " images.");
                else if (!busy) fileInput.current?.click();
              }}
            >
              <Paperclip size={16} />
            </button>
            {/* Voice input (feat/voice-input) mounts its mic button here, next to 📎. */}
          </div>
          <details className="composer-more">
            <summary>
              <MoreHorizontal size={15} aria-hidden />
              <span className="sr-only">Agent, model and computer: </span>
              <span>
                {agentName(agent)}
                {model ? ` · ${model}` : ""} · {device?.name}
              </span>
            </summary>
            <div className="conversation-composer-options">
              <select
                aria-label="Agent"
                value={agent}
                disabled={busy || discoveringModels}
                onChange={(e) => {
                  setAgent(e.target.value);
                  setAttachNote("");
                  setModel("");
                  setModelJob(null);
                }}
              >
                {!HARNESSES.includes(agent as (typeof HARNESSES)[number]) && (
                  <option value={agent} disabled>
                    {agentName(agent)}
                  </option>
                )}
                {HARNESSES.map((a) => (
                  <option
                    key={a}
                    value={a}
                    disabled={!!device && !device.harnesses?.includes(a)}
                  >
                    {agentName(a)}
                  </option>
                ))}
              </select>
              <input
                list={`models-${session.id}`}
                aria-label="Model"
                placeholder="Default model"
                value={model}
                disabled={busy || discoveringModels}
                onChange={(e) => setModel(e.target.value)}
              />
              <datalist id={`models-${session.id}`}>
                {modelJob?.agent === agent &&
                  modelJob.result?.items?.map((item) => (
                    <option key={item.id} value={item.id}>
                      {item.name}
                    </option>
                  ))}
              </datalist>
              <button
                type="button"
                title="Load models from your computer"
                aria-label="Load models from your computer"
                disabled={!device || busy || discoveringModels}
                onClick={() => {
                  void models();
                }}
              >
                <RefreshCw size={14} />
              </button>
              {available.length > 1 && (
                <select
                  aria-label="Computer"
                  value={device?.id || ""}
                  disabled={busy || discoveringModels}
                  onChange={(e) => {
                    setDeviceId(e.target.value);
                    setModelJob(null);
                    setModel("");
                  }}
                >
                  {available.map((d) => (
                    <option key={d.id} value={d.id}>
                      {d.name}
                    </option>
                  ))}
                </select>
              )}
            </div>
            {discoveringModels && (
              <p className="chat-muted" role="status">
                Loading models…{" "}
                <button
                  type="button"
                  onClick={() => {
                    if (modelJob)
                      void desktopApi
                        .cancel(modelJob.id)
                        .then(setModelJob)
                        .catch((e) => setError(message(e)));
                  }}
                >
                  Cancel
                </button>
              </p>
            )}
            {modelJob?.error && <p className="chat-error">{modelJob.error}</p>}
            {modelJob?.result?.warning && (
              <p className="chat-muted">{modelJob.result.warning}</p>
            )}
          </details>
          {/* One round button: Send, which turns into Stop while the agent works. */}
          <button
            className={`chat-send ${busy ? "is-stop" : ""}`}
            type="submit"
            aria-label={busy ? (sending ? "Sending" : "Stop reply") : "Send"}
            title={
              busy
                ? sending
                  ? "Sending…"
                  : touch
                    ? "Stop"
                    : `Stop · Esc`
                : touch
                  ? "Send"
                  : "Send · Enter (Shift+Enter for a new line)"
            }
            disabled={
              busy
                ? sending || !job
                : !device ||
                  discoveringModels ||
                  !device.harnesses.includes(agent) ||
                  !draft.trim() ||
                  readingImages > 0 ||
                  (images.length > 0 && !!imageBlocker)
            }
          >
            <span className="chat-send-icon is-send" aria-hidden>
              <ArrowUp size={16} strokeWidth={2.4} />
            </span>
            <span className="chat-send-icon is-stop" aria-hidden>
              <Square size={12} fill="currentColor" />
            </span>
          </button>
        </div>
      </form>
      ) : (
        <p className="conversation-composer-note" role="note">
          <Monitor size={14} />
          {replyNote}
        </p>
      )}
      {shareOpen && (
        <AccessibleModal
          labelledBy="share-chat-title"
          onClose={() => setShareOpen(false)}
        >
          <header>
            <h3 id="share-chat-title">Share chat</h3>
            <button
              aria-label="Close sharing"
              onClick={() => setShareOpen(false)}
            >
              <X size={18} />
            </button>
          </header>
          <p>
            Your teammate can read this chat, including future messages. They
            can't run anything on your computer.
          </p>
          <label>
            Teammate
            <select
              value={shareUser}
              onChange={(e) => setShareUser(e.target.value)}
            >
              <option value="">Select a person</option>
              {recipientList.map((person) => (
                <option key={person.user_id} value={person.user_id}>
                  {person.name} ·{" "}
                  {person.project_role || person.organization_role || "Member"}
                </option>
              ))}
            </select>
          </label>
          {(recipients.error || share.error) && (
            <p role="alert" className="chat-error">
              {message(recipients.error || share.error)}
            </p>
          )}
          {recipients.isPending && <p role="status">Loading teammates…</p>}
          {!recipients.isPending && !recipients.error && !recipientList.length && (
            <p>
              Nobody else is in this project yet. Add people on the{" "}
              <Link to="/app/team">Team</Link> page first.
            </p>
          )}
          <button
            className="chat-send is-wide"
            disabled={!shareUser || share.isPending}
            onClick={() => share.mutate()}
          >
            {share.isPending ? "Sharing…" : "Share with teammate"}
          </button>
        </AccessibleModal>
      )}
    </section>
  );
}

/** What's left to say once a reply finished. Success is silent. */
function JobOutcome({
  job,
  canReply,
  onContinue,
  onRetry,
}: {
  job: DesktopJob;
  canReply: boolean;
  onContinue: () => void;
  onRetry: () => void;
}) {
  if (job.state === "completed")
    return job.result?.cloud_saved === false ? (
      <p className="chat-live-status" role="status">
        Saved on your computer; syncing to your account needs attention.
      </p>
    ) : null;
  if (job.state === "cancelled")
    return (
      <p className="chat-live-status is-stopped" role="status">
        Stopped. The steps above already ran; changes they made to files stay.
        {canReply && (
          <button type="button" className="chat-inline-action" onClick={onContinue}>
            Continue
          </button>
        )}
      </p>
    );
  if (job.state === "expired")
    return (
      <p className="chat-live-status is-stopped" role="status">
        Request expired. It was not automatically retried.
        {canReply && (
          <button type="button" className="chat-inline-action" onClick={onRetry}>
            Retry
          </button>
        )}
      </p>
    );
  return null;
}
