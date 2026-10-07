package holons

func selectedModel(model []string) string {
	if len(model) == 0 {
		return ""
	}
	return model[0]
}

// AgentSelection is the launch preference snapshot stored with a conversation.
type AgentSelection struct {
	Model       string
	Permissions string
}

func (h Holon) actionSelection(sourceID string, selection AgentSelection) AgentSelection {
	if sourceID != "" {
		source := h.AgentSession(sourceID)
		return AgentSelection{Model: source.Model, Permissions: source.Permissions}
	}
	return selection
}
