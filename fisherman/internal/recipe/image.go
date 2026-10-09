package recipe

import (
	"errors"
	"strings"
)

// ErrImageRequired is ValidateImage's answer for an empty image on a host
// that did not boot from live media.
var ErrImageRequired = errors.New(`image is required: this system did not boot from live media, ` +
	`so there is no running image to install (an empty "image" means ` +
	`"install the booted live image")`)

// ValidateImage checks the image against the host: an empty image means
// "install the image this live system is running" (bootc installs the booted
// container), which only makes sense on live media. On any other host it
// used to reach `bootc install` after the disk was already partitioned.
//
// live is the host's answer, from probe's live-media detection; Validate
// stays free of host reads, so the CLI makes this second call.
func (r *Recipe) ValidateImage(live bool) error {
	if strings.TrimSpace(r.Image) != "" || live {
		return nil
	}
	return ErrImageRequired
}
