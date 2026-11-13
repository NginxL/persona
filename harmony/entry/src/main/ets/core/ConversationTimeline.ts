export interface TimelineMessage {
  readonly id: string;
  readonly role: string;
  readonly at: number;
}
export interface TimelineCard {
  readonly id: string;
  readonly at: number;
}
export interface TimelineEntry {
  readonly id: string;
  readonly messageId: string;
  readonly cardId: string;
}

/** A card follows its exchange, before the next input, so it never splits a streamed answer. */
export function mergeConversationTimeline(messages: readonly TimelineMessage[], cards: readonly TimelineCard[]): TimelineEntry[] {
  const byId = new Map<string, TimelineCard>();
  for (const card of cards) if (card.id) byId.set(card.id, card);
  const ordered = [...byId.values()].sort((a, b) => a.at - b.at || a.id.localeCompare(b.id));
  const result: TimelineEntry[] = [];
  let cursor = 0;
  const appendCard = (card: TimelineCard): void => { result.push({ id: 'feed:' + card.id, messageId: '', cardId: card.id }); };
  for (const message of messages) {
    if (message.role === 'user' && message.at > 0) {
      while (cursor < ordered.length && ordered[cursor].at < message.at) appendCard(ordered[cursor++]);
    }
    result.push({ id: 'message:' + message.id, messageId: message.id, cardId: '' });
  }
  while (cursor < ordered.length) appendCard(ordered[cursor++]);
  return result;
}
