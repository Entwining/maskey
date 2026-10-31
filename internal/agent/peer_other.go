//go:build !darwin || !arm64

package agent

import "net"

// Peer authentication requires macOS on Apple Silicon.
func authenticate(conn *net.UnixConn) (int, error) {
	return 0, errPeerUnavailable
}
