package larkcli

import "time"

// cliLocation is the zone lark-cli prints wall-clock times in ("2006-01-02 15:04"): the zone of the machine running
// it, which is the hub's. That is the one place the machine's zone is right here (internal/tzguard allows this file).
func cliLocation() *time.Location { return time.Local }
