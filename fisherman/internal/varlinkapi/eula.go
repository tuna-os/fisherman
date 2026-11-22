package varlinkapi

import (
	"os"
)

// DefaultEULAPath is the one shared license every frontend reads: an
// AppStream component carrying an agreement of type eula.
const DefaultEULAPath = "/usr/share/tuna-installer/eula.xml"

// EULAFileEnv overrides the license path (testing, derivative media).
const EULAFileEnv = "TUNA_EULA_FILE"

// ResolveEULA finds the shared license: TUNA_EULA_FILE wins, otherwise the
// default path. Available is true only when the file exists, reads and is
// not empty; no file means the frontend skips the page. The frontend parses
// the AppStream XML itself.
func ResolveEULA() Eula {
	path := os.Getenv(EULAFileEnv)
	if path == "" {
		path = DefaultEULAPath
	}
	fi, err := os.Stat(path)
	if err != nil || fi.IsDir() || fi.Size() == 0 {
		return Eula{Available: false}
	}
	if f, err := os.Open(path); err != nil {
		return Eula{Available: false}
	} else {
		f.Close()
	}
	return Eula{Available: true, Path: ptr(path)}
}
