package control

import "strings"

// SerialRevoked reports whether serial (any case) is on the cluster CRL.
func (s State) SerialRevoked(serial string) bool {
	_, ok := s.CRL[strings.ToUpper(serial)]
	return ok
}
