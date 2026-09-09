package reverse

import (
	"crypto/rand"
	"io"
	mathrand "math/rand"
)

func (c *Control) FillInRandom() {
	randomLength := mathrand.Intn(64)
	randomLength++
	c.Random = make([]byte, randomLength)
	io.ReadFull(rand.Reader, c.Random)
}
