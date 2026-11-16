package agentproto

// AsyncQuestion is a question carried by a nonblocking agent-message
// notification. Its ID identifies the question; it is not a native RPC request.
type AsyncQuestion struct {
	ID      string   `json:"id,omitempty"`
	Title   string   `json:"title"`
	Options []string `json:"options,omitempty"`
}

// AsyncQuestionAnswer is an accepted native user-message reply, not a command
// to submit an answer or a native RPC response.
type AsyncQuestionAnswer struct {
	QuestionItemID string `json:"questionItemId"`
	Answer         string `json:"answer"`
}
