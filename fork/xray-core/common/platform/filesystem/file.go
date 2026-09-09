package filesystem

import (
	"os"
	"path/filepath"

	"github.com/xtls/xray-core/common/buf"
	"github.com/xtls/xray-core/common/platform"
)

func ReadFile(path string) ([]byte, error) {
	reader, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer reader.Close()

	return buf.ReadAllToBytes(reader)
}

func ReadCert(file string) ([]byte, error) {
	if filepath.IsAbs(file) {
		return ReadFile(file)
	}
	return ReadFile(platform.GetCertLocation(file))
}
