// Package repository checks repository metadata for the assistant. Its shared
// identity and task binding contract remains available through type aliases.
package repository

import "github.com/nwverse/persona/server/internal/repositorywire"

type Identity = repositorywire.Identity

var ErrRemote = repositorywire.ErrRemote

func Parse(remote, provider string) (Identity, error) { return repositorywire.Parse(remote, provider) }
