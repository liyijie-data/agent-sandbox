import type { AttachmentMeta, Conversation, Message } from "../api/types";

export interface SendInput {
  prompt: string;
  attachments: AttachmentMeta[];
  fileIds: string[];
  skillIds: string[];
  toolIds: string[];
  reasoningEffort?: string;
  contextWindowTokens?: number;
  maxOutputTokens?: number;
  modelParameters?: Record<string, unknown>;
}

export type MutateMessages = (
  updater: (messages: Message[]) => Message[],
) => void;

export interface UseRunStreamOptions {
  getActive: () => Conversation | null;
  createConversation: (title: string) => Promise<Conversation>;
  setMessages: MutateMessages;
  refreshThread: () => Promise<void>;
  clearResources: () => void;
}
