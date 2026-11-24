import { useEffect, useRef, useState } from "react";
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
  GitFork,
  Monitor,
  Plus,
  RefreshCw,
  Search,
  Send,
  Share2,
  Square,
  X,
} from "lucide-react";
import { chatsApi, type ChatSession, type ChatTurn } from "@/api/chats";
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
} from "@/components/ChatMessage";
import { ChatDiff } from "@/components/ChatDiff";
import { ProjectSwitcher } from "@/components/ProjectSwitcher";
import { NexusMark } from "@/components/NexusBrand";
import { AccessibleModal } from "@/components/ui/AccessibleModal";
import { useAuth } from "@/providers/AuthProvider";
import { readableChatText } from "@/lib/chat-content";
import { DESKTOP_RELAY } from "@/lib/env";
import { agentName, chatTitle, composerHarness } from "@/lib/harness-message";
import { ApiError } from "@/types/api";
import { formatRelative } from "@/utils/format";

const message = (error: unknown) =>
  error instanceof Error ? error.message : "Request failed. Try again.";
const HARNESSES = ["codex", "cursor", "opencode", "claude"] as const;

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
export function ChatsPage() {
  const { projectId, setProjectId } = useAuth();
  const cache = useQueryClient();
  const [params, setParams] = useSearchParams();
  const id = params.get("chat") || "";
  // Home's search box links here with ?q=.
  const [search, setSearch] = useState(() => params.get("q") ?? ""),
    [query, setQuery] = useState(() => params.get("q") ?? "");
  const feed = useInfiniteQuery({
    queryKey: ["chats", projectId, search],
    enabled: !!projectId,
    initialPageParam: "",
    queryFn: ({ pageParam, signal }) =>
      chatsApi.list(projectId!, search, pageParam, signal),
    getNextPageParam: (last) => last.next_cursor || undefined,
  });
  const cards = feed.data?.pages.flatMap((page) => page.items) ?? [];
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
  useEffect(() => {
    const timer = setTimeout(() => setSearch(query), 250);
    return () => clearTimeout(timer);
  }, [query]);
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
        <ProjectSwitcher compact />
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
                : "No chats yet. Install the desktop app and chat with Claude Code, Codex or Cursor in this project — your chats show up here."}
            </p>
          )}
          {cards.map((card) => (
            <button
              key={card.session_id}
              className={`conversation-card ${id === card.session_id ? "is-active" : ""}`}
              aria-current={id === card.session_id ? "true" : undefined}
              onClick={() => setParams({ chat: card.session_id })}
            >
              <span>
                <strong>{chatTitle(card.title, card.summary, card.harness)}</strong>
                {card.parent_session_id && (
                  <GitFork size={13} aria-label="Forked chat" />
                )}
              </span>
              {card.summary && <p>{readableChatText(card.summary)}</p>}
              <small>
                {agentName(card.harness)}
                <time dateTime={card.last_active_at}>
                  {formatRelative(card.last_active_at)}
                </time>
              </small>
            </button>
          ))}
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
        <p className="chat-muted conversation-state" role="status">Loading chat…</p>
      ) : session.error ? (
        <div role="alert" className="chat-error conversation-state">
          <p>
            {session.error instanceof ApiError && session.error.status === 404
              ? "This chat was deleted or you no longer have access."
              : `Couldn't open this chat. ${message(session.error)}`}
          </p>
          <button onClick={() => setParams({})}>Back to chats</button>
        </div>
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
            onBack={() => setParams({})}
            onOpen={(next) => setParams({ chat: next })}
          />
        )
      )}
    </div>
  );
}

function Conversation({
  session,
  onBack,
  onOpen,
}: {
  session: ChatSession;
  onBack: () => void;
  onOpen: (id: string) => void;
}) {
  const { user } = useAuth();
  const owned = !!user?.userId && session.owner_user_id === user.userId;
  const cache = useQueryClient();
  const [tab, setTab] = useState("chat"),
    [findOpen, setFindOpen] = useState(false),
    [find, setFind] = useState(""),
    [findQuery, setFindQuery] = useState(""),
    [matchIndex, setMatchIndex] = useState(0);
  const [shareOpen, setShareOpen] = useState(false),
    [shareUser, setShareUser] = useState("");
  const [agent, setAgent] = useState(composerHarness(session.harness)),
    [model, setModel] = useState(""),
    [deviceId, setDeviceId] = useState(""),
    [draft, setDraft] = useState("");
  const [ownJob, setJob] = useState<DesktopJob | null>(null),
    [error, setError] = useState(""),
    [streamNonce, setStreamNonce] = useState(0);
  const [modelJob, setModelJob] = useState<DesktopJob | null>(null);
  const [sending, setSending] = useState(false);
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
  const busy = sending || (!!job && !isFinished(job.state));
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
    if (followReply.current && scroll.current && job)
      scroll.current.scrollTop = scroll.current.scrollHeight;
  }, [job]);
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
    node.scrollTo({
      top: node.scrollHeight,
      behavior: window.matchMedia("(prefers-reduced-motion: reduce)").matches ? "auto" : "smooth",
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
  async function send() {
    if (
      !owned ||
      !device ||
      busy ||
      discoveringModels ||
      modelLock.current ||
      sendingLock.current ||
      !draft.trim()
    )
      return;
    sendingLock.current = true;
    setSending(true);
    setError("");
    try {
      const next = await desktopApi.create({
        id: crypto.randomUUID(),
        device_id: device.id,
        session_id: session.id,
        project_id: session.project_id,
        kind: "turn",
        agent,
        model,
        prompt: draft.trim(),
      });
      sentPrompt.current = draft.trim();
      setJob(next);
      setDraft("");
    } catch (e) {
      setError(message(e));
    } finally {
      sendingLock.current = false;
      setSending(false);
    }
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
        behavior: window.matchMedia("(prefers-reduced-motion: reduce)").matches
          ? "instant"
          : "smooth",
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
  const responseAlreadySynced =
    !!job?.result?.answer_cursor &&
    rows.some(
      (turn) =>
        turn.role === "assistant" &&
        turn.cursor_idx === job.result?.answer_cursor,
    );
  const liveTurns: ChatTurn[] =
    job && !responseAlreadySynced
      ? [
          {
            idx: -2,
            role: "user",
            text_preview: job.prompt || sentPrompt.current,
          },
          {
            idx: -1,
            role: "assistant",
            text_preview: job.result?.text || job.text,
            tool_calls: job.activities,
          },
        ]
      : [];
  return (
    <section className="conversation-detail" aria-label="Conversation">
      <header className="conversation-header">
        <div>
          <button className="chat-mobile-back" onClick={onBack}>
            ← Chats
          </button>
          <h2>{chatTitle(session.title, session.summary, session.harness)}</h2>
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
            title="Find in chat"
            aria-label="Find in chat"
            onClick={() => setFindOpen(!findOpen)}
          >
            <Search size={17} />
          </button>
          <button
            disabled={fork.isPending || busy}
            title="Copy this chat into a new one you can continue"
            aria-label="Fork chat"
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
          followReply.current = distance < 100;
          setAwayFromLatest(distance > 600);
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
        {turns.isPending && <p className="chat-muted" role="status">Loading messages…</p>}
        {tab === "memory" ? (
          <div className="conversation-memory">
            <h3>Conversation summary</h3>
            <p>
              {summary.isPending
                ? "Loading summary…"
                : summary.error
                  ? message(summary.error)
                  : readableChatText(summary.data?.summary || session.summary || "") ||
                    "No summary yet. Nexus writes one after the chat has a few turns."}
            </p>
            <h3>Saved memories</h3>
            {memory.isPending ? (
              <p>Loading memories…</p>
            ) : memory.error ? (
              <p role="alert">{message(memory.error)}</p>
            ) : memory.data?.items.length ? (
              memory.data.items.map((item, i) => {
                const row = item as { content?: string; key?: string };
                return (
                  <article key={i}>
                    <strong>{row.key}</strong>
                    <p>{row.content || "No readable content was saved."}</p>
                  </article>
                );
              })
            ) : (
              <p>No memories were saved from this chat.</p>
            )}
          </div>
        ) : (
          <div className="conversation-transcript">
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
              : segments.map((segment) =>
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
                    />
                  ),
                )}
            {!segments.length && !turns.isPending && !turns.error && (
              <p className="chat-muted">
                {findQuery
                  ? `No messages match “${findQuery}”.`
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
            {liveTurns.map((turn) => (
              <ChatMessage
                key={turn.idx}
                turn={turn}
                harness={job?.agent || agent}
              />
            ))}
            {files.map((file) => (
              <ChatDiff key={file.path} {...file} />
            ))}
            {job?.error && (
              <p role="alert" className="chat-error">
                {job.error}
              </p>
            )}
            {job && (
              <p className="chat-live-status" role="status">
                {job.state === "completed"
                  ? job.result?.cloud_saved === false
                    ? "Saved on Desktop; cloud sync needs attention."
                    : "Reply complete"
                  : job.state === "cancelled"
                    ? "Cancelled. Changes already made to files are not undone."
                    : job.state === "expired"
                      ? "Request expired. It was not automatically retried."
                      : job.status || job.state}
              </p>
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
        {awayFromLatest && tab === "chat" && !findQuery && (
          <button type="button" className="chat-jump-latest" onClick={jumpToLatest}>
            <ArrowDown size={14} /> Latest
          </button>
        )}
      </div>
      {DESKTOP_RELAY && (
      <form
        className="conversation-composer"
        onSubmit={(e) => {
          e.preventDefault();
          void send();
        }}
      >
        <div className="conversation-composer-options">
          <select
            aria-label="Harness"
            value={agent}
            disabled={busy || discoveringModels}
            onChange={(e) => {
              setAgent(e.target.value);
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
            placeholder="Harness default model"
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
            title="Load models from Desktop"
            aria-label="Load models from Desktop"
            disabled={
              !device || busy || (!!modelJob && !isFinished(modelJob.state))
            }
            onClick={() => {
              void models();
            }}
          >
            <RefreshCw size={14} />
          </button>
          {available.length > 1 && (
            <select
              aria-label="Execution device"
              value={device?.id || ""}
              disabled={busy || (!!modelJob && !isFinished(modelJob.state))}
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
        <textarea
          rows={2}
          maxLength={16000}
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
          placeholder="Message the agent…"
          aria-label="Message the agent"
          onKeyDown={(e) => {
            if (
              e.key === "Enter" &&
              !e.shiftKey &&
              !e.nativeEvent.isComposing
            ) {
              e.preventDefault();
              void send();
            }
          }}
        />
        <div className="conversation-composer-footer">
          <small>
            <Monitor size={13} />
            {!owned
              ? "Shared with you. Fork it to continue in your own chat."
              : device
                ? `Runs on ${device.name}. You approve each request in Desktop.`
                : "To send from here, open Nexus Desktop with this project and turn on browser requests in its Settings."}
          </small>
          {busy ? (
            <button
              type="button"
              disabled={sending || !job}
              onClick={() => {
                if (job)
                  void desktopApi
                    .cancel(job.id)
                    .then(setJob)
                    .catch((e) => setError(message(e)));
              }}
            >
              <Square size={14} />
              {sending ? "Sending…" : "Stop"}
            </button>
          ) : (
            <button
              className="chat-send"
              type="submit"
              disabled={
                !owned ||
                !device ||
                discoveringModels ||
                !device.harnesses.includes(agent) ||
                !draft.trim()
              }
            >
              <Send size={15} />
              Send
            </button>
          )}
        </div>
        {discoveringModels && (
          <p className="chat-muted" role="status">
            Loading models from Desktop…{" "}
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
              Cancel discovery
            </button>
          </p>
        )}
        {modelJob?.error && <p className="chat-error">{modelJob.error}</p>}
        {modelJob?.result?.warning && (
          <p className="chat-muted">{modelJob.result.warning}</p>
        )}
        {devices.error && (
          <small className="chat-muted" role="status">
            {devices.error instanceof ApiError && devices.error.status >= 500
              ? "Sending from the browser isn't available on this server right now."
              : `Couldn't check your desktops. ${message(devices.error)}`}
          </small>
        )}
      </form>
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
            className="chat-send"
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
