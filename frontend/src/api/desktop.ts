import { apiRequest } from "@/lib/api-client";
import { API_BASE } from "@/lib/env";
import { storage } from "@/utils/storage";
import { splitDesktopEvents } from "@/lib/desktop-events";

export type DesktopDevice = {
  id: string;
  name: string;
  projects: string[];
  harnesses: string[];
  last_seen_at: string;
  /** Relay version the desktop speaks; 2+ accepts images. */
  protocol?: number;
};
/** An image sent with a turn. data (base64) only goes up; responses carry metadata. */
export type DesktopAttachment = {
  name: string;
  media_type: string;
  data?: string;
  size?: number;
  sha256?: string;
};
export type DesktopActivity = {
  id: string;
  tool: string;
  status: string;
  command?: string;
  input?: string;
  output?: string;
  error?: string;
  files?: string[];
  diff?: string;
};
export type DesktopResult = {
  text?: string;
  agent?: string;
  answer_cursor?: string;
  cloud_saved?: boolean;
  file_diffs?: { path: string; diff: string; notice?: string }[];
  items?: { id: string; name: string }[];
  warning?: string;
};
export type DesktopJob = {
  id: string;
  device_id: string;
  session_id: string;
  project_id: string;
  kind: string;
  agent: string;
  model?: string;
  prompt?: string;
  attachments?: DesktopAttachment[];
  state: string;
  revision: number;
  status?: string;
  text?: string;
  error?: string;
  activities?: DesktopActivity[];
  result?: DesktopResult;
};
export function isFinished(state: string) {
  return ["completed", "failed", "cancelled", "expired"].includes(state);
}
export const desktopApi = {
  devices(signal?: AbortSignal) {
    return apiRequest<{ items: DesktopDevice[] }>("/v1/desktop/devices", {
      signal,
    });
  },
  create(body: {
    id: string;
    device_id: string;
    project_id: string;
    session_id: string;
    kind: "turn" | "models";
    agent: string;
    model?: string;
    prompt?: string;
    attachments?: DesktopAttachment[];
  }) {
    return apiRequest<DesktopJob>("/v1/desktop/jobs", { method: "POST", body });
  },
  get(id: string, signal?: AbortSignal) {
    return apiRequest<DesktopJob>(
      `/v1/desktop/jobs/${encodeURIComponent(id)}`,
      { signal },
    );
  },
  cancel(id: string) {
    return apiRequest<DesktopJob>(
      `/v1/desktop/jobs/${encodeURIComponent(id)}/cancel`,
      { method: "POST", body: {} },
    );
  },
};

// Complete snapshots make reconnection lossless. Tokens stay in an auth header,
// never an EventSource query string, history entry or copied share URL.
export async function watchDesktopJob(
  id: string,
  signal: AbortSignal,
  onUpdate: (job: DesktopJob) => void,
) {
  let revision = -1;
  while (!signal.aborted) {
    const token = storage.getToken();
    if (!token)
      throw new Error("Sign in again to follow this desktop request.");
    const response = await fetch(
      `${API_BASE}/v1/desktop/jobs/${encodeURIComponent(id)}/events`,
      {
        headers: {
          Authorization: `Bearer ${token}`,
          Accept: "text/event-stream",
        },
        signal,
      },
    );
    if (!response.ok || !response.body)
      throw new Error(
        `Live connection failed (${response.status}). The request may still be running in Desktop.`,
      );
    const reader = response.body.getReader(),
      decoder = new TextDecoder();
    let pending = "";
    try {
      while (!signal.aborted) {
        const { value, done } = await reader.read();
        if (done) break;
        const frames = splitDesktopEvents(
          pending,
          decoder.decode(value, { stream: true }),
        );
        pending = frames.pending;
        for (const data of frames.data) {
          const job = JSON.parse(data) as DesktopJob;
          if (job.id !== id || !Number.isSafeInteger(job.revision))
            throw new Error("Invalid live response.");
          if (job.revision > revision) {
            revision = job.revision;
            onUpdate(job);
          }
          if (isFinished(job.state)) return;
        }
      }
    } finally {
      await reader.cancel().catch(() => {});
      reader.releaseLock();
    }
    // Server deliberately renews the stream every 25 seconds. A reconnect is
    // read-only and cannot enqueue or repeat an agent turn.
    const current = await desktopApi.get(id, signal);
    if (current.revision > revision) {
      revision = current.revision;
      onUpdate(current);
    }
    if (isFinished(current.state)) return;
    await new Promise<void>((resolve, reject) => {
      const abort = () => {
        clearTimeout(timer);
        reject(new DOMException("Aborted", "AbortError"));
      };
      const timer = setTimeout(() => {
        signal.removeEventListener("abort", abort);
        resolve();
      }, 800);
      signal.addEventListener("abort", abort, { once: true });
      if (signal.aborted) abort();
    });
  }
}
