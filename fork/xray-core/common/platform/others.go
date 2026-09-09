//go:build !windows
// +build !windows

package platform

import (
	"os"
	"path/filepath"
)

func LineSeparator() string {
	return "\n"
}

// GetAssetLocation searches for `file` in the env dir, the executable dir, and certain locations
func GetAssetLocation(file string) string {
	assetPath := GetEnv(AssetLocation, getExecutableDir())
	defPath := filepath.Join(assetPath, file)
	for _, p := range []string{
		defPath,
		filepath.Join("/usr/local/share/xray/", file),
		filepath.Join("/usr/share/xray/", file),
		filepath.Join("/opt/share/xray/", file),
	} {
		if _, err := os.Stat(p); os.IsNotExist(err) {
			continue
		}

		// asset found
		return p
	}

	// asset not found, let the caller throw out the error
	return defPath
}

// GetCertLocation searches for `file` in the env dir and the executable dir
func GetCertLocation(file string) string {
	certPath := GetEnv(CertLocation, getExecutableDir())
	return filepath.Join(certPath, file)
}
