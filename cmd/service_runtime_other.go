//go:build !linux

package cmd

func ensureServiceRuntimeExecutable() error {
	return nil
}

func cleanupServiceRuntimeExecutable() error {
	return nil
}
