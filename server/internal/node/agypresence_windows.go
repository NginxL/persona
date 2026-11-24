package node

// agyPresenceHeld: not probed on Windows (agy's presence lock there is not a flock the node can test).
func agyPresenceHeld(dir, conversation string) bool { return false }
