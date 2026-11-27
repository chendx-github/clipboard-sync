//go:build !windows

package main

// ensureSingleInstance is a no-op outside Windows; the Linux agent is started
// from a single supervisor (pm2 / systemd) instead.
func ensureSingleInstance() error {
	return nil
}
