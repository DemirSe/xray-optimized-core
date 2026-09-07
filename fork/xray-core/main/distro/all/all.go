package all

import (
	// The following are necessary as they register handlers in their init functions.

	// Mandatory features. Can't remove unless there are replacements.
	_ "github.com/xtls/xray-core/app/dispatcher"
	_ "github.com/xtls/xray-core/app/proxyman/inbound"
	_ "github.com/xtls/xray-core/app/proxyman/outbound"

	// Other optional features.
	_ "github.com/xtls/xray-core/app/log"
	_ "github.com/xtls/xray-core/app/policy"
	_ "github.com/xtls/xray-core/app/reverse"

	// Fix dependency cycle caused by core import in internet package
	_ "github.com/xtls/xray-core/transport/internet/tagged/taggedimpl"

	// Inbound and outbound proxies.
	_ "github.com/xtls/xray-core/proxy/freedom"
	_ "github.com/xtls/xray-core/proxy/vless/inbound"

	// Transports
	_ "github.com/xtls/xray-core/transport/internet/reality"
	_ "github.com/xtls/xray-core/transport/internet/tcp"
	_ "github.com/xtls/xray-core/transport/internet/tls"
	_ "github.com/xtls/xray-core/transport/internet/udp"

	// Transport headers
	_ "github.com/xtls/xray-core/transport/internet/headers/noop"

	// JSON only (no TOML/YAML)
	_ "github.com/xtls/xray-core/main/json"

	// File/stdin/http config loader (EffectiveConfigFileLoader dosya okumayı da kurar, ŞART!)
	_ "github.com/xtls/xray-core/main/confloader/external"

	// Commands (trimmed: tls, uuid, x25519 only)
	_ "github.com/xtls/xray-core/main/commands/trimmed"
)
