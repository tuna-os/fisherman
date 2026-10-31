//go:build !linux

package install

import "fmt"

// Browser-bound media verification requires Linux descriptor-relative opens.
func PreflightBoundOCIMediaInput(m *MediaInput) error {
	return fmt.Errorf("strict bound OCI media verification requires Linux")
}
