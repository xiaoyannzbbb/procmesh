package health

import (
	"context"
	"errors"
	"net"
	"testing"
)

type closeErrorConn struct {
	net.Conn
}

func (closeErrorConn) Close() error {
	return errors.New("close failed")
}

type stubDialer struct {
	conn net.Conn
}

func (d stubDialer) DialContext(context.Context, string, string) (net.Conn, error) {
	return d.conn, nil
}

func TestCheckTCPWithDialerIgnoresCloseError(t *testing.T) {
	err := checkTCPWithDialer(context.Background(), "unused", stubDialer{conn: closeErrorConn{}})
	if err != nil {
		t.Fatalf("successful dial must be healthy even when Close fails: %v", err)
	}
}
