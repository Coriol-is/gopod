package runner

// Per-chat model selection (CP-008). The choice is stored by cmd/gopod in
// router_state and restored at boot via SetChatModel; the flag syntax is
// the provider's business (ModelArgs).

// ModelForChat returns the chat's model override, "" for provider default.
func (r *Runner) ModelForChat(chatFolder string) string {
	r.providersMu.RLock()
	defer r.providersMu.RUnlock()
	return r.chatModels[chatFolder]
}

// SetChatModel sets or clears ("") the chat's model override. Takes
// effect on the next turn; no container restart needed.
func (r *Runner) SetChatModel(chatFolder, model string) {
	r.providersMu.Lock()
	defer r.providersMu.Unlock()
	if model == "" {
		delete(r.chatModels, chatFolder)
		return
	}
	if r.chatModels == nil {
		r.chatModels = make(map[string]string)
	}
	r.chatModels[chatFolder] = model
}

// withModel inserts prov.ModelArgs(model) before the prompt, which is
// the final argument of every provider's RunCmd. Claude passes the
// prompt as `-p <prompt>`, so the flag goes before `-p`; Codex takes a
// bare positional prompt, and `exec resume` accepts -m before it.
func withModel(cmd []string, prov AgentProvider, model string) []string {
	args := prov.ModelArgs(model)
	if len(args) == 0 || len(cmd) == 0 {
		return cmd
	}
	cut := len(cmd) - 1
	if cut >= 1 && cmd[cut-1] == "-p" {
		cut--
	}
	out := make([]string, 0, len(cmd)+len(args))
	out = append(out, cmd[:cut]...)
	out = append(out, args...)
	return append(out, cmd[cut:]...)
}
