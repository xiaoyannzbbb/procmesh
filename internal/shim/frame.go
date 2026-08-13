package shim

import (
	"encoding/binary"
	"io"

	"github.com/qleelulu/procmesh/internal/errcode"
)

const maxFrameSize = 16 << 20

// WriteFrame writes a 4-byte big-endian length prefix followed by payload.
// Rejects payloads larger than 16MiB with errcode.INVALID.
func WriteFrame(w io.Writer, payload []byte) error {
	if len(payload) > maxFrameSize {
		return errcode.E(errcode.INVALID, "frame too large")
	}
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(payload)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}

// ReadFrame reads a length-prefixed frame from r.
// Rejects frames larger than 16MiB with errcode.INVALID.
func ReadFrame(r io.Reader) ([]byte, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n > maxFrameSize {
		return nil, errcode.E(errcode.INVALID, "frame too large")
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	return buf, nil
}
