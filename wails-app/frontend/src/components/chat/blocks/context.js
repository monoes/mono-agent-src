import { createContext, useContext } from 'react'

// What the chat hosting a block lets it do. Provided by the chat view; absent
// (null) in every other context, where blocks are display-only.
//   onSend(text) -> Promise<boolean>  send a user message; undefined = cannot send now
//   onAppLink(href)                   open a monoagent:// link; optional
//   conversationId                    keys locally stored answers
export const ChatBlocksHost = createContext(null)

// Where a block sits: scope identifies the message (turn id + text part), turn
// is the turn whose blocks are answered together, live is whether that turn is
// still streaming. Provided by ChatTimeline.
export const ChatBlocksScope = createContext({ scope: '', turn: '', live: false })

export function useBlockEnv() {
  const host = useContext(ChatBlocksHost) || {}
  const { scope, turn = '', live } = useContext(ChatBlocksScope)
  return { onSend: host.onSend, onAppLink: host.onAppLink, conversationId: host.conversationId || '', scope, turn, live }
}
