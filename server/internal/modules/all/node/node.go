// Package node is the node's composition root of the built-in modules (docs/architecture/core-and-modules.md
// "Provider seams"): the modules that also run on the user's computers, each added by a file of its own whose build
// constraint leaves it out with persona_without_<id> or persona_core_only, provide what the node needs through the
// node's ports. The node's program links this package (internal/nodecmd), never internal/modules/all, which is the
// manager's.
package node
