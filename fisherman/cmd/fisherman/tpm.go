package main

import (
	"errors"
	"fmt"

	"github.com/tuna-os/fisherman/internal/luks"
	"github.com/tuna-os/fisherman/internal/post"
)

// stageTPM2Enrollment stages the first-boot TPM2 enrolment in the installed
// deployment of the target mounted at target, and returns the line to log.
// It never fails the install: without the enrolment the passphrase (or the
// recovery key) still unlocks the disk, and the line says so.
//
// rootPart is the LUKS partition, luksUUID its UUID, and passphrase a key
// that unlocks it now. The passphrase is used to add a single-use keyslot
// and is not written anywhere (luks.StageFirstBootEnrollment).
func stageTPM2Enrollment(target, rootPart, luksUUID, passphrase string) string {
	const fallback = "the passphrase or recovery key still unlocks the disk"
	d, err := post.ResolveDeployment(target)
	if err == nil {
		err = luks.StageFirstBootEnrollment(d, rootPart, luksUUID, passphrase)
	}
	switch {
	case err == nil:
		return "TPM2 auto-unlock will be enrolled on first boot"
	case errors.Is(err, luks.ErrNoCryptenroll), errors.Is(err, luks.ErrTransientEtc):
		return fmt.Sprintf("TPM2 auto-unlock skipped: %v; %s", err, fallback)
	default:
		return fmt.Sprintf("Warning: could not stage first-boot TPM2 enrollment (%s): %v", fallback, err)
	}
}
