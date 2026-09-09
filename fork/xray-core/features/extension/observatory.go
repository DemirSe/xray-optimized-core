package extension

import (
	"context"

	"github.com/xtls/xray-core/features"
	"google.golang.org/protobuf/proto"
)

type Observatory interface {
	features.Feature

	GetObservation(ctx context.Context) (proto.Message, error)
}

// ponytail: BurstObservatory deleted (zero in-tree impls); burst check is a
// structural assert at the single call site instead of a named interface.

func ObservatoryType() interface{} {
	return (*Observatory)(nil)
}
