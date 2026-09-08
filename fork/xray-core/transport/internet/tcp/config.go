package tcp

import (
	"github.com/xtls/xray-core/transport/internet"
)

func init() {
	if err := internet.RegisterProtocolConfigCreator(protocolName, func() interface{} {
		return new(Config)
	}); err != nil {
		panic(err)
	}
}
