package crypto

import (
	"crypto/aes"
	"crypto/cipher"

	"github.com/xtls/xray-core/common"
)

// NewAesGcm creates a AEAD cipher based on AES-GCM.
func NewAesGcm(key []byte) cipher.AEAD {
	block := common.Must2(aes.NewCipher(key))
	aead := common.Must2(cipher.NewGCM(block))
	return aead
}
