import type { Conversation, Message } from "./types";
import { request } from "./client";

export function listConversations(): Promise<Conversation[]> {
  return request<Conversation[]>("/api/conversations");
}

export function createConversation(title: string): Promise<Conversation> {
  return request<Conversation>("/api/conversations", {
    method: "POST",
    body: JSON.stringify({ title }),
  });
}

export function listMessages(conversationId: string): Promise<Message[]> {
  return request<Message[]>(`/api/conversations/${conversationId}/messages`);
}
