//go:build windows

package node

import "os"

func externalAdapterPlatformSupported() bool { return false }

func externalAdapterTrustedPath(string, bool, bool) (string, error) {
	return "", externalAdapterFailure("adapter_platform_unsupported")
}

func externalAdapterOpenConfig(string) (*os.File, string, error) {
	return nil, "", externalAdapterFailure("adapter_platform_unsupported")
}
