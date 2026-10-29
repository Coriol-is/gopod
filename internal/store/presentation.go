package store

// UploadPresentationVoice is carried in the ParseMode field of an upload part
// (an upload has no parse mode) to ask the transport for a Telegram voice
// message instead of a document. It is set only by the host when it builds a
// plan for audio it generated itself; the strict V1 agent request has no field
// that reaches it, so an agent-supplied file is always a document.
const UploadPresentationVoice = "voice"
