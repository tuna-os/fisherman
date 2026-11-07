//go:build !linux

package install

import "fmt"

// Browser-bound media verification requires Linux descriptor-relative opens.
func PreflightBoundOCIMediaInput(m *MediaInput) error {
	return fmt.Errorf("strict bound OCI media verification requires Linux")
}

// Bound store-source verification has the same Linux requirement.
func PreflightBoundStoreMediaInput(m *MediaInput) error {
	return fmt.Errorf("strict bound store media verification requires Linux")
}
