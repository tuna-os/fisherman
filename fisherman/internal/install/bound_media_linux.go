//go:build linux

package install

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
)

const boundMetadataLimit int64 = 8 << 20
const boundBlobLimit int64 = 16 << 30

// Proposed first-candidate policy, not a claim that every image fits.
const boundLayerLimit = 1024
const boundUniqueBlobLimit int64 = 10 << 30

// This seam is replaced only by filesystem fixture tests. Production requires
// an actually read-only mounted filesystem, not a caller-supplied assertion.
var boundReadOnlyCheck = func(fd int) error {
	var stat syscall.Statfs_t
	if err := syscall.Fstatfs(fd, &stat); err != nil {
		return err
	}
	const stReadonly = 1 // Linux statfs ST_RDONLY.
	if stat.Flags&stReadonly == 0 {
		return fmt.Errorf("bound OCI layout is not on a read-only filesystem")
	}
	return nil
}

// PreflightBoundOCIMediaInput is a strict gate for an explicitly bound browser
// OCI source. It leaves PreflightOCILayout and standalone OCI semantics alone.
// Layer bytes are streamed, never buffered. All opens are relative to held
// directory descriptors with O_NOFOLLOW on every path component. Read-only
// status is checked on each final file too: nested mounts can differ from root.
func PreflightBoundOCIMediaInput(m *MediaInput) error {
	if m == nil {
		return fmt.Errorf("missing bound media input")
	}
	if err := m.Validate(); err != nil {
		return err
	}
	if m.SchemaVersion != 1 || m.Image == "" || m.TargetImgref == "" ||
		!validSHA256Digest(m.PlatformManifestDigest) {
		return fmt.Errorf("bound media needs schema v1, image, target and platform manifest digest")
	}
	if m.Platform != runtime.GOOS+"/"+runtime.GOARCH {
		return fmt.Errorf("bound media platform %q does not match host", m.Platform)
	}
	p := m.OCILayoutPath
	if p == "/" || !filepath.IsAbs(p) || filepath.Clean(p) != p {
		return fmt.Errorf("bound OCI layout path is not canonical absolute")
	}
	fd, err := syscall.Open("/", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	root, err := openBoundRelative(fd, strings.TrimPrefix(p, "/"), true)
	_ = syscall.Close(fd)
	if err != nil {
		return err
	}
	defer root.Close()
	if err := boundReadOnlyCheck(int(root.Fd())); err != nil {
		return err
	}
	layoutRaw, err := readBoundMetadata(root, "oci-layout")
	if err != nil {
		return err
	}
	var layout struct {
		ImageLayoutVersion string `json:"imageLayoutVersion"`
	}
	if err := json.Unmarshal(layoutRaw, &layout); err != nil || layout.ImageLayoutVersion != "1.0.0" {
		return fmt.Errorf("invalid bound oci-layout")
	}
	indexRaw, err := readBoundMetadata(root, "index.json")
	if err != nil {
		return err
	}
	var index ociIndex
	if err := json.Unmarshal(indexRaw, &index); err != nil || index.SchemaVersion != 2 {
		return fmt.Errorf("invalid bound index")
	}
	selected, err := selectOCIManifest(index.Manifests, m.PlatformManifestDigest)
	if err != nil {
		return err
	}
	if p := selected.Platform; p != nil &&
		(p.OS != runtime.GOOS || p.Architecture != runtime.GOARCH) {
		return fmt.Errorf("bound index platform does not match host")
	}
	blobs := boundBlobReader{root: root, sizes: make(map[string]int64), verified: make(map[string][]byte)}
	manifestRaw, err := blobs.read(selected, true)
	if err != nil {
		return err
	}
	var manifest ociManifest
	if err := json.Unmarshal(manifestRaw, &manifest); err != nil || manifest.SchemaVersion != 2 {
		return fmt.Errorf("invalid bound platform manifest")
	}
	if m.ConfigDigest != "" && manifest.Config.Digest != m.ConfigDigest {
		return fmt.Errorf("bound config digest differs from media input")
	}
	if len(manifest.Layers) == 0 || len(manifest.Layers) > boundLayerLimit {
		return fmt.Errorf("bound platform manifest layer count exceeds policy: %d", len(manifest.Layers))
	}
	// Validate the entire declared work budget before opening config/layers.
	if err := blobs.reserve(manifest.Config, true); err != nil {
		return err
	}
	for _, layer := range manifest.Layers {
		if err := blobs.reserve(layer, false); err != nil {
			return err
		}
	}
	configRaw, err := blobs.read(manifest.Config, true)
	if err != nil {
		return err
	}
	var config struct {
		OS           string `json:"os"`
		Architecture string `json:"architecture"`
	}
	if err := json.Unmarshal(configRaw, &config); err != nil ||
		config.OS != runtime.GOOS || config.Architecture != runtime.GOARCH {
		return fmt.Errorf("bound image config platform does not match host")
	}
	for _, layer := range manifest.Layers {
		if _, err := blobs.read(layer, false); err != nil {
			return err
		}
	}
	return nil
}

// openBoundRelative rejects symlinks, noncanonical components, and nonregular
// files. O_NONBLOCK prevents a hostile FIFO from blocking before fstat.
func openBoundRelative(base int, relative string, directory bool) (*os.File, error) {
	if relative == "" || filepath.IsAbs(relative) || filepath.Clean(relative) != relative {
		return nil, fmt.Errorf("invalid bound relative path %q", relative)
	}
	fd, err := syscall.Dup(base)
	if err != nil {
		return nil, err
	}
	parts := strings.Split(relative, "/")
	for i, part := range parts {
		if part == "" || part == "." || part == ".." {
			_ = syscall.Close(fd)
			return nil, fmt.Errorf("invalid bound path component")
		}
		flags := syscall.O_RDONLY | syscall.O_CLOEXEC | syscall.O_NOFOLLOW | syscall.O_NONBLOCK
		if i < len(parts)-1 || directory {
			flags |= syscall.O_DIRECTORY
		}
		next, openErr := syscall.Openat(fd, part, flags, 0)
		_ = syscall.Close(fd)
		if openErr != nil {
			return nil, fmt.Errorf("opening bound path %q: %w", relative, openErr)
		}
		fd = next
	}
	file := os.NewFile(uintptr(fd), relative)
	stat, err := file.Stat()
	if err != nil || (!directory && !stat.Mode().IsRegular()) {
		_ = file.Close()
		return nil, fmt.Errorf("bound path %q is not a regular file/directory", relative)
	}
	return file, nil
}

func readBoundMetadata(root *os.File, name string) ([]byte, error) {
	file, err := openBoundRelative(int(root.Fd()), name, false)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if err := boundReadOnlyCheck(int(file.Fd())); err != nil {
		return nil, err
	}
	raw, err := io.ReadAll(io.LimitReader(file, boundMetadataLimit+1))
	if err != nil || int64(len(raw)) > boundMetadataLimit {
		return nil, fmt.Errorf("bound metadata %q unreadable or oversized", name)
	}
	return raw, nil
}

func readBoundBlob(root *os.File, desc ociDescriptor, metadata bool) ([]byte, error) {
	if !validSHA256Digest(desc.Digest) {
		return nil, fmt.Errorf("bound blob digest is not sha256: %q", desc.Digest)
	}
	limit := boundBlobLimit
	if metadata {
		limit = boundMetadataLimit
	}
	if desc.Size < 0 || desc.Size > limit {
		return nil, fmt.Errorf("bound blob size exceeds policy: %d", desc.Size)
	}
	file, err := openBoundRelative(int(root.Fd()), "blobs/sha256/"+strings.TrimPrefix(desc.Digest, "sha256:"), false)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if err := boundReadOnlyCheck(int(file.Fd())); err != nil {
		return nil, err
	}
	hash := sha256.New()
	var buffer bytes.Buffer
	var writer io.Writer = hash
	if metadata {
		writer = io.MultiWriter(hash, &buffer)
	}
	count, err := io.Copy(writer, io.LimitReader(file, desc.Size+1))
	if err != nil || count != desc.Size {
		return nil, fmt.Errorf("bound blob size mismatch: %s", desc.Digest)
	}
	if fmt.Sprintf("sha256:%x", hash.Sum(nil)) != desc.Digest {
		return nil, fmt.Errorf("bound blob hash mismatch: %s", desc.Digest)
	}
	return buffer.Bytes(), nil
}

// boundBlobReader accounts for manifest/config/layers together, once per digest.
// This instance is local to one verification, never reused across operations.
// Only already-hashed metadata bytes are cached; layer bytes are not retained.
type boundBlobReader struct {
	root     *os.File
	sizes    map[string]int64
	verified map[string][]byte
	total    int64
}

func (r *boundBlobReader) reserve(desc ociDescriptor, metadata bool) error {
	if !validSHA256Digest(desc.Digest) {
		return fmt.Errorf("bound blob digest is not sha256: %q", desc.Digest)
	}
	limit := boundBlobLimit
	if metadata {
		limit = boundMetadataLimit
	}
	if desc.Size < 0 || desc.Size > limit {
		return fmt.Errorf("bound blob size exceeds policy: %d", desc.Size)
	}
	if old, exists := r.sizes[desc.Digest]; exists {
		if old != desc.Size {
			return fmt.Errorf("inconsistent repeated bound blob size: %s", desc.Digest)
		}
		return nil
	}
	if desc.Size > boundUniqueBlobLimit-r.total {
		return fmt.Errorf("bound unique blob bytes exceed policy: %d", boundUniqueBlobLimit)
	}
	r.sizes[desc.Digest] = desc.Size
	r.total += desc.Size
	return nil
}

func (r *boundBlobReader) read(desc ociDescriptor, metadata bool) ([]byte, error) {
	if err := r.reserve(desc, metadata); err != nil {
		return nil, err
	}
	if raw, exists := r.verified[desc.Digest]; exists {
		// Metadata is always consumed before layers. Refuse a future caller
		// attempting to promote an unbuffered layer rather than rehashing it.
		if metadata && raw == nil {
			return nil, fmt.Errorf("bound blob metadata requested after unbuffered verification")
		}
		return raw, nil
	}
	raw, err := readBoundBlob(r.root, desc, metadata)
	if err != nil {
		return nil, err
	}
	r.verified[desc.Digest] = raw
	return raw, nil
}
