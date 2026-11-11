package agentproto

// AsyncQuestion is a question carried by a nonblocking agent-message
// notification. Its ID identifies the question; it is not a native RPC request.
type AsyncQuestion struct {
	ID      string   `json:"id,omitempty"`
	Title   string   `json:"title"`
	Options []string `json:"options,omitempty"`
}
