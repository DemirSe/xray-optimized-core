package protocol

import (
	"math/rand"
	"time"
)

type Timestamp int64

type TimestampGenerator func() Timestamp

func NowTime() Timestamp {
	return Timestamp(time.Now().Unix())
}

func NewTimestampGenerator(base Timestamp, delta int) TimestampGenerator {
	return func() Timestamp {
		rangeInDelta := rand.Intn(delta*2) - delta
		return base + Timestamp(rangeInDelta)
	}
}
