package all

import "github.com/nwverse/persona/server/internal/assistant/kernel"

func init() { kernel.Provide(Builtin) }
