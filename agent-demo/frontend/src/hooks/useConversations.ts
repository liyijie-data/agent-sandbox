import { useCallback, useRef, useState } from "react";
import {
  createConversation,
  listConversations,
  listMessages,
} from "../api/conversations";
import { listArtifacts } from "../api/runs";
import type { Artifact, Conversation, Message } from "../api/types";

function dedupe(artifacts: Artifact[]): Artifact[] {
  return [...new Map(artifacts.map((item) => [item.id, item])).values()];
}

export function useConversations() {
  const [conversations, setConversations] = useState<Conversation[]>([]);
  const [active, setActive] = useState<Conversation | null>(null);
  const [messages, setMessages] = useState<Message[]>([]);
  const [artifactsByRun, setArtifactsByRun] = useState<
    Record<string, Artifact[]>
  >({});
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const activeRef = useRef<Conversation | null>(null);
  activeRef.current = active;

  const setThread = useCallback((thread: Message[]) => {
    setMessages(thread);
  }, []);

  const loadArtifactsFor = useCallback(async (thread: Message[]) => {
    const runIds = [
      ...new Set(
        thread
          .filter((m) => m.role === "assistant" && m.run_id)
          .map((m) => m.run_id as string),
      ),
    ];
    if (!runIds.length) {
      setArtifactsByRun({});
      return;
    }
    const lists = await Promise.all(
      runIds.map(async (runId) => [runId, await listArtifacts(runId)] as const),
    );
    setArtifactsByRun(
      Object.fromEntries(lists.map(([runId, items]) => [runId, dedupe(items)])),
    );
  }, []);

  const select = useCallback(
    async (conversation: Conversation) => {
      setActive(conversation);
      setError(null);
      try {
        const history = await listMessages(conversation.id);
        setThread(history);
        void loadArtifactsFor(history).catch(() => undefined);
      } catch (err) {
        setError(err instanceof Error ? err.message : String(err));
      }
    },
    [loadArtifactsFor, setThread],
  );

  const refreshThread = useCallback(async () => {
    const target = activeRef.current;
    if (!target) return;
    try {
      const history = await listMessages(target.id);
      setThread(history);
      await loadArtifactsFor(history);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  }, [loadArtifactsFor, setThread]);

  const newConversation = useCallback(() => {
    setActive(null);
    setThread([]);
    setArtifactsByRun({});
  }, [setThread]);

  const createLocalConversation = useCallback(
    async (title: string): Promise<Conversation> => {
      const conversation = await createConversation(title);
      setConversations((x) => [conversation, ...x]);
      setActive(conversation);
      setThread([]);
      setArtifactsByRun({});
      return conversation;
    },
    [setThread],
  );

  const loadAll = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const list = await listConversations();
      setConversations(list);
      if (list[0]) await select(list[0]);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setLoading(false);
    }
  }, [select]);

  return {
    conversations,
    active,
    messages,
    artifactsByRun,
    loading,
    error,
    loadAll,
    select,
    newConversation,
    createLocalConversation,
    refreshThread,
    setMessages,
  };
}