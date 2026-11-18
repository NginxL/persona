package proto

// UsageBucket is one minute of one model's token usage on a machine, summed over every transcript there (node
// usage.go). A node reports a bucket's whole total each time it changes, so reporting it again replaces it. Runtime-only agents instead send per-request deltas
// in a usage notification with add: true; those buckets are added and survive a file-meter full window.
type UsageBucket struct {
	Minute int64  `json:"minute"` // Unix seconds of the minute's start
	Model  string `json:"model"`
	// Launcher ran the replies: the session's launcher as Persona started it, "" when unknown (typed in a terminal, an
	// older node). A minute has one bucket per model, launcher and agent.
	Launcher string `json:"launcher,omitempty"`
	// Agent ran the replies: Session.Agent's names ("claude", "codex", "traex", …; tokenuse); "" from an older node,
	// and for the usage the manager stored before it kept agents apart.
	Agent      string `json:"agent,omitempty"`
	Input      int64  `json:"in,omitempty"` // not from the cache
	CacheWrite int64  `json:"cache_write,omitempty"`
	CacheRead  int64  `json:"cache_read,omitempty"`
	Output     int64  `json:"out,omitempty"`
	Requests   int    `json:"req,omitempty"` // model replies
}
