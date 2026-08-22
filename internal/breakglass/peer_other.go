//go:build !linux && !darwin

package breakglass

import (
	"fmt"
	"net"
)

func lookupPeer(net.Conn) (Peer, error) {
	return Peer{}, fmt.Errorf("break-glass peer credentials are unsupported on this platform")
}
