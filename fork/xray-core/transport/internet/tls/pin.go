package tls

import (
	"crypto/sha256"
)

// []byte must be ASN.1 DER content
// ponytail: one []byte func (was 2 generics); pass cert.Raw, hex-encode at call site.
func GenerateCertHash(raw []byte) []byte {
	sum := sha256.Sum256(raw)
	return sum[:]
}
