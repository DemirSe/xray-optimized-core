package trimmed

import (
	"github.com/xtls/xray-core/main/commands/all/tls"
	"github.com/xtls/xray-core/main/commands/base"
)

func init() {
	base.RootCommand.Commands = append(
		base.RootCommand.Commands,
		tls.CmdTLS,
		cmdUUID,
		cmdX25519,
	)
}
