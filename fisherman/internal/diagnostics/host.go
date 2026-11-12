package diagnostics

import (
	"bufio"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Versions is the software the bundle names.
type Versions struct {
	Fisherman string `json:"fisherman"`
	Go        string `json:"go"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	// Kernel is the running kernel release.
	Kernel string `json:"kernel,omitempty"`
	// OSRelease holds the allowlisted keys of the host's os-release:
	// ID, ID_LIKE, VERSION_ID, VARIANT_ID. Other keys (names, URLs) are
	// left out.
	OSRelease map[string]string `json:"os_release,omitempty"`
}

// osReleaseKeys are the os-release keys the bundle keeps.
var osReleaseKeys = map[string]bool{"ID": true, "ID_LIKE": true, "VERSION_ID": true, "VARIANT_ID": true}

// osReleaseValue is the shape of a kept os-release value.
var osReleaseValue = regexp.MustCompile(`^[A-Za-z0-9._ -]{1,64}$`)

// maxHostFile bounds every host file the export reads.
const maxHostFile = 1 << 20

func readBounded(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, maxHostFile))
}

func hostVersions(root string, v Versions) Versions {
	if b, err := readBounded(filepath.Join(root, "proc/sys/kernel/osrelease")); err == nil {
		v.Kernel = token(strings.TrimSpace(string(b)), wordRe)
	}
	for _, p := range []string{"etc/os-release", "usr/lib/os-release"} {
		b, err := readBounded(filepath.Join(root, p))
		if err != nil {
			continue
		}
		v.OSRelease = map[string]string{}
		sc := bufio.NewScanner(bytes.NewReader(b))
		for sc.Scan() {
			k, val, ok := strings.Cut(sc.Text(), "=")
			if !ok || !osReleaseKeys[k] {
				continue
			}
			if uq, err := strconv.Unquote(val); err == nil {
				val = uq
			} else {
				val = strings.Trim(val, `'"`)
			}
			v.OSRelease[k] = token(val, osReleaseValue)
		}
		break
	}
	return v
}

// Mounts is what is still mounted or mapped from an install: the mounts at
// and under the target and the scratch directory, and the LUKS mapper.
type Mounts struct {
	// Entries are the mounts at or under Target or Scratch, in mountinfo
	// order.
	Entries []MountEntry `json:"entries"`
	// Mapper is true when the LUKS mapper device (fisherman-root, or the
	// recipe's luksMapperName) exists.
	Mapper bool `json:"luks_mapper_open"`
	// OtherMappers is how many other device-mapper devices exist. Their
	// names are not in the bundle.
	OtherMappers int `json:"other_mappers"`
	// Truncated is true when there were more than MaxMounts entries.
	Truncated bool `json:"truncated,omitempty"`
}

// MountEntry is one mount.
type MountEntry struct {
	// Under is "target" or "scratch"; Path is the mount point relative to
	// it, as an absolute path ("/" for the base itself, "/boot").
	Under    string `json:"under"`
	Path     string `json:"path"`
	Fstype   string `json:"fstype"`
	Source   string `json:"source"`
	ReadOnly bool   `json:"read_only"`
}

// MaxMounts bounds Mounts.Entries.
const MaxMounts = 256

// DefaultTargetMount, DefaultScratch and DefaultLuksMapper are the install's
// defaults (cmd/fisherman/main.go).
const (
	DefaultTargetMount = "/mnt/fisherman-target"
	DefaultScratch     = "/var/fisherman-tmp"
	DefaultLuksMapper  = "fisherman-root"
)

// unescapeMountinfo decodes the octal escapes of /proc/self/mountinfo
// ("\040" is a space).
func unescapeMountinfo(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

var fstypeRe = regexp.MustCompile(`^[a-z0-9._-]{1,32}$`)

func hostMounts(root, target, mapper string, rd *Redactor) Mounts {
	m := Mounts{Entries: []MountEntry{}}
	bases := []struct{ name, path string }{{"target", target}, {"scratch", DefaultScratch}}
	if b, err := readBounded(filepath.Join(root, "proc/self/mountinfo")); err == nil {
		sc := bufio.NewScanner(bytes.NewReader(b))
		sc.Buffer(make([]byte, 0, 64<<10), 64<<10)
		for sc.Scan() {
			pre, post, ok := strings.Cut(sc.Text(), " - ")
			if !ok {
				continue
			}
			f, g := strings.Fields(pre), strings.Fields(post)
			if len(f) < 6 || len(g) < 2 {
				continue
			}
			mp := filepath.Clean(unescapeMountinfo(f[4]))
			for _, base := range bases {
				rel, err := filepath.Rel(base.path, mp)
				if err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
					continue
				}
				if len(m.Entries) == MaxMounts {
					m.Truncated = true
					break
				}
				src := unescapeMountinfo(g[1])
				if strings.HasPrefix(src, "/dev/") {
					src = rd.Ident("device", src)
				} else {
					src = token(src, fstypeRe)
				}
				rel = filepath.Clean("/" + rel)
				m.Entries = append(m.Entries, MountEntry{Under: base.name, Path: token(rel, pathRe),
					Fstype: token(g[0], fstypeRe), Source: src, ReadOnly: hasOption(f[5], "ro")})
				break
			}
		}
	}
	names, _ := filepath.Glob(filepath.Join(root, "sys/block/dm-*/dm/name"))
	sort.Strings(names)
	for _, n := range names {
		b, err := readBounded(n)
		if err != nil {
			continue
		}
		if strings.TrimSpace(string(b)) == mapper {
			m.Mapper = true
		} else {
			m.OtherMappers++
		}
	}
	return m
}

func hasOption(opts, want string) bool {
	for _, o := range strings.Split(opts, ",") {
		if o == want {
			return true
		}
	}
	return false
}
