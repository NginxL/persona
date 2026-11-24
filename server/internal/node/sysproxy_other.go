//go:build !darwin

package node

import "context"

func readSystemProxy(context.Context) systemProxy {
	return systemProxy{}
}
