package filesystem

import (
	"os"
	"path/filepath"

	"github.com/xtls/xray-core/common/platform"
)

func ReadCert(file string) ([]byte, error) {
	if filepath.IsAbs(file) {
		return os.ReadFile(file)
	}
	return os.ReadFile(platform.GetCertLocation(file))
}
