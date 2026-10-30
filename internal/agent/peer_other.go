//go:build !darwin || !arm64

package agent

import "net"

// Peer authentication requires macOS on Apple Silicon.
func authenticate(conn *net.UnixConn) error {
	return errPeerUnavailable
}
