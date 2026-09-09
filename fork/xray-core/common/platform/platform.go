package platform // import "github.com/xtls/xray-core/common/platform"

import (
	"os"
	"path/filepath"
)

const (
	ConfigLocation  = "xray.location.config"
	ConfdirLocation = "xray.location.confdir"
	AssetLocation   = "xray.location.asset"
	CertLocation    = "xray.location.cert"

	UseReadV         = "xray.buf.readv"
	UseFreedomSplice = "xray.buf.splice"
	UseVmessPadding  = "xray.vmess.padding"
	UseCone          = "xray.cone.disabled"

	BufferSize           = "xray.ray.buffer.size"
	BrowserDialerAddress = "xray.browser.dialer"
	XUDPLog              = "xray.xudp.show"
	XUDPBaseKey          = "xray.xudp.basekey"

	TunFdKey = "xray.tun.fd"

	MphCachePath = "xray.mph.cache"
)

type EnvFlag struct {
	Name string
}

func NewEnvFlag(name string) EnvFlag {
	return EnvFlag{
		Name: name,
	}
}

// ponytail: single os.Getenv; no XRAY_* alt-name knob is set live (verified),
// empty env falls back to default like an unset one.
func (f EnvFlag) GetValue(defaultValue string) string {
	if v := os.Getenv(f.Name); v != "" {
		return v
	}

	return defaultValue
}

func getExecutableDir() string {
	exec, err := os.Executable()
	if err != nil {
		return ""
	}
	return filepath.Dir(exec)
}

func GetConfigurationPath() string {
	configPath := NewEnvFlag(ConfigLocation).GetValue(getExecutableDir())
	return filepath.Join(configPath, "config.json")
}

// GetConfDirPath reads "xray.location.confdir"
func GetConfDirPath() string {
	configPath := NewEnvFlag(ConfdirLocation).GetValue("")
	return configPath
}
