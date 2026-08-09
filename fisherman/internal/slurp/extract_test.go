package slurp

// Tests for the data/wallpaper extraction + injection half (data.go,
// wallpaper.go). Everything filesystem-shaped runs against temp dirs; the
// only external seam is the runner package's RunFn/OutputFn, which is
// stubbed here. No root, no NTFS partition, no /run access needed.

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tuna-os/fisherman/internal/runner"
)

// fakeOutput returns out for every Output call.
func fakeOutput(t *testing.T, out []byte) {
	t.Helper()
	orig := runner.OutputFn
	runner.OutputFn = func(name string, args ...string) ([]byte, error) { return out, nil }
	t.Cleanup(func() { runner.OutputFn = orig })
}

// fakeRun records calls and fails any call whose joined args contain a
// whole fail token; returns a thunk for the recorded calls.
func fakeRun(t *testing.T, failTokens ...string) func() []string {
	t.Helper()
	orig := runner.RunFn
	var calls []string
	runner.RunFn = func(_ io.Reader, name string, args ...string) error {
		joined := name + " " + strings.Join(args, " ")
		calls = append(calls, joined)
		padded := " " + joined + " "
		for _, tok := range failTokens {
			if strings.Contains(padded, " "+tok+" ") {
				return errors.New("mocked failure: " + tok)
			}
		}
		return nil
	}
	t.Cleanup(func() { runner.RunFn = orig })
	return func() []string { return calls }
}

// withScratchDirs points scratchBase and ntfsMountPoint at fresh temp dirs
// and restores them afterwards.
func withScratchDirs(t *testing.T) (scratch, mountPoint string) {
	t.Helper()
	scratch = t.TempDir()
	mountPoint = t.TempDir()
	origS, origM := scratchBase, ntfsMountPoint
	scratchBase, ntfsMountPoint = scratch, mountPoint
	t.Cleanup(func() { scratchBase, ntfsMountPoint = origS, origM })
	return scratch, mountPoint
}

func writeFixture(t *testing.T, path string, size int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
}

// --- extractFromProfile / extractFromDir helpers ---

func TestExtractFromProfile_TranscodedWallpaper(t *testing.T) {
	profile := t.TempDir()
	target := t.TempDir()
	// A real wallpaper-sized TranscodedWallpaper is copied.
	writeFixture(t, filepath.Join(profile,
		"AppData", "Roaming", "Microsoft", "Windows", "Themes", "TranscodedWallpaper"), 200*1024)

	count := extractFromProfile(profile, target)
	if count != 1 {
		t.Fatalf("count = %d, want 1", count)
	}
	if _, err := os.Stat(filepath.Join(target, "TranscodedWallpaper")); err != nil {
		t.Errorf("TranscodedWallpaper not copied: %v", err)
	}
}

func TestExtractFromProfile_SkipsTinyTranscodedWallpaper(t *testing.T) {
	profile := t.TempDir()
	target := t.TempDir()
	writeFixture(t, filepath.Join(profile,
		"AppData", "Roaming", "Microsoft", "Windows", "Themes", "TranscodedWallpaper"), 1024)

	if count := extractFromProfile(profile, target); count != 0 {
		t.Errorf("count = %d, want 0 (file <10KB is not a wallpaper)", count)
	}
}

func TestExtractFromProfile_SpotlightAssetsWithoutExtension(t *testing.T) {
	profile := t.TempDir()
	target := t.TempDir()
	assets := filepath.Join(profile,
		"AppData", "Local", "Packages",
		"Microsoft.Windows.ContentDeliveryManager_cw5n1h2txyewy", "LocalState", "Assets")
	// Ext-less file, plausible wallpaper size -> renamed .jpg.
	writeFixture(t, filepath.Join(assets, "spotlight01"), 300*1024)
	// Ext-less but too small -> skipped.
	writeFixture(t, filepath.Join(assets, "thumb02"), 20*1024)

	count := extractFromProfile(profile, target)
	if count != 1 {
		t.Fatalf("count = %d, want 1", count)
	}
	if _, err := os.Stat(filepath.Join(target, "spotlight01.jpg")); err != nil {
		t.Errorf("ext-less asset should be copied with .jpg suffix: %v", err)
	}
	if _, err := os.Stat(filepath.Join(target, "thumb02.jpg")); err == nil {
		t.Error("small ext-less asset must be skipped")
	}
}

func TestExtractFromProfile_CachedFilesAndPictures(t *testing.T) {
	profile := t.TempDir()
	target := t.TempDir()
	writeFixture(t, filepath.Join(profile,
		"AppData", "Roaming", "Microsoft", "Windows", "Themes", "CachedFiles", "cached.jpg"), 100*1024)
	writeFixture(t, filepath.Join(profile, "Pictures", "Wallpapers", "mine.png"), 100*1024)

	count := extractFromProfile(profile, target)
	if count != 2 {
		t.Fatalf("count = %d, want 2 (CachedFiles + Pictures/Wallpapers)", count)
	}
	for _, name := range []string{"cached.jpg", "mine.png"} {
		if _, err := os.Stat(filepath.Join(target, name)); err != nil {
			t.Errorf("%s not copied: %v", name, err)
		}
	}
}

func TestExtractFromDir_DedupsSameNamedFiles(t *testing.T) {
	src := t.TempDir()
	target := t.TempDir()
	writeFixture(t, filepath.Join(src, "wall.jpg"), 100*1024)
	writeFixture(t, filepath.Join(src, "wall.jpg"), 100*1024) // overwrites, same name

	count := extractFromDir(src, target, 10)
	if count != 1 {
		t.Fatalf("count = %d, want 1", count)
	}
	if _, err := os.Stat(filepath.Join(target, "wall.jpg")); err != nil {
		t.Errorf("wall.jpg not copied: %v", err)
	}
}

func TestExtractFromDir_ExcludesNonImageAndOversized(t *testing.T) {
	src := t.TempDir()
	target := t.TempDir()
	writeFixture(t, filepath.Join(src, "a.png"), 100*1024)
	writeFixture(t, filepath.Join(src, "b.txt"), 100*1024)        // wrong extension
	writeFixture(t, filepath.Join(src, "huge.bmp"), 30*1024*1024) // >20MB cap

	count := extractFromDir(src, target, 10)
	if count != 1 {
		t.Fatalf("count = %d, want 1 (only a.png)", count)
	}
}

func TestExtractWallpapersFromPictures_Subdirs(t *testing.T) {
	pictures := t.TempDir()
	target := t.TempDir()
	writeFixture(t, filepath.Join(pictures, "Wallpapers", "w1.jpg"), 100*1024)
	writeFixture(t, filepath.Join(pictures, "backgrounds", "w2.jpg"), 100*1024)
	writeFixture(t, filepath.Join(pictures, "random", "w3.jpg"), 100*1024) // ignored

	count := extractWallpapersFromPictures(pictures, target)
	if count != 2 {
		t.Fatalf("count = %d, want 2", count)
	}
}

// --- md5hex / thumbnail generation ---

func TestMd5Hex(t *testing.T) {
	got := md5hex("file:///usr/share/backgrounds/a.jpg")
	if len(got) != 32 {
		t.Fatalf("md5hex length = %d, want 32 (got %q)", len(got), got)
	}
	// Deterministic and different for different inputs.
	if got == md5hex("file:///usr/share/backgrounds/b.jpg") {
		t.Error("md5hex must differ for different URIs")
	}
}

func TestDetectThumbnailer_PreferenceOrder(t *testing.T) {
	// gdk-pixbuf-thumbnailer first, then ffmpeg, then convert.
	calls := 0
	orig := runner.OutputFn
	runner.OutputFn = func(name string, args ...string) ([]byte, error) {
		calls++
		if strings.Contains(strings.Join(args, " "), "gdk-pixbuf-thumbnailer") {
			return nil, errors.New("not found")
		}
		return []byte("/usr/bin/ffmpeg"), nil
	}
	t.Cleanup(func() { runner.OutputFn = orig })

	if got := detectThumbnailer(); got != "ffmpeg" {
		t.Errorf("detectThumbnailer = %q, want ffmpeg", got)
	}
	if calls != 2 {
		t.Errorf("expected 2 which probes, got %d", calls)
	}
}

func TestDetectThumbnailer_NoneAvailable(t *testing.T) {
	orig := runner.OutputFn
	runner.OutputFn = func(name string, args ...string) ([]byte, error) {
		return nil, errors.New("not found")
	}
	t.Cleanup(func() { runner.OutputFn = orig })

	if got := detectThumbnailer(); got != "" {
		t.Errorf("detectThumbnailer = %q, want empty when nothing is installed", got)
	}
}

func TestGenerateSingleThumbnail_ArgsAndResult(t *testing.T) {
	calls := fakeRun(t)
	if !generateSingleThumbnail("ffmpeg", "/src/a.jpg", "/dst/a.png") {
		t.Fatal("generateSingleThumbnail should succeed when the runner does")
	}
	joined := strings.Join(calls(), " ")
	if !strings.Contains(joined, "ffmpeg -y -i /src/a.jpg") ||
		!strings.Contains(joined, "scale=256:256") {
		t.Errorf("ffmpeg invocation wrong: %s", joined)
	}

	// Failure propagates as false.
	failCalls := fakeRun(t, "convert")
	if generateSingleThumbnail("convert", "/src/b.jpg", "/dst/b.png") {
		t.Error("expected false when the thumbnailer command fails")
	}
	_ = failCalls
}

func TestGenerateSystemThumbnails(t *testing.T) {
	target := t.TempDir()
	// System background + injected wallpaper; tiny file and non-image skipped.
	writeFixture(t, filepath.Join(target, "usr", "share", "backgrounds", "a.jpg"), 200*1024)
	writeFixture(t, filepath.Join(target, "usr", "share", "backgrounds", "tiny.png"), 2*1024)
	writeFixture(t, filepath.Join(target, "usr", "share", "backgrounds", "notes.txt"), 200*1024)
	writeFixture(t, filepath.Join(target, "var", "home", ".windows-wallpapers", "b.jpg"), 200*1024)

	fakeOutput(t, []byte("/usr/bin/gdk-pixbuf-thumbnailer"))
	// The thumbnailer command is mocked, so mirror its side effect: create
	// the destination file (the last arg) so the cache dir really fills up.
	orig := runner.RunFn
	runner.RunFn = func(_ io.Reader, name string, args ...string) error {
		if name == "gdk-pixbuf-thumbnailer" {
			return os.WriteFile(args[len(args)-1], []byte("png"), 0o644)
		}
		return nil
	}
	t.Cleanup(func() { runner.RunFn = orig })

	count := GenerateSystemThumbnails(target, false)
	if count != 2 {
		t.Fatalf("count = %d, want 2 thumbnails", count)
	}
	cacheDir := filepath.Join(target, "var", "home", ".cache", "thumbnails", "large")
	entries, err := os.ReadDir(cacheDir)
	if err != nil || len(entries) != 2 {
		t.Fatalf("cache dir entries = %v, err=%v; want 2 .png thumbs", entries, err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".png" {
			t.Errorf("thumbnail %q lacks .png suffix", e.Name())
		}
	}
}

func TestGenerateSystemThumbnails_NoThumbnailer(t *testing.T) {
	target := t.TempDir()
	writeFixture(t, filepath.Join(target, "usr", "share", "backgrounds", "a.jpg"), 200*1024)

	orig := runner.OutputFn
	runner.OutputFn = func(name string, args ...string) ([]byte, error) {
		return nil, errors.New("not found")
	}
	t.Cleanup(func() { runner.OutputFn = orig })

	if count := GenerateSystemThumbnails(target, false); count != 0 {
		t.Errorf("count = %d, want 0 with no thumbnailer", count)
	}
}

// --- ExtractWallpapers ---

func TestExtractWallpapers_FullFlow(t *testing.T) {
	scratch, mountPoint := withScratchDirs(t)
	writeFixture(t, filepath.Join(mountPoint, "Users", "Alice",
		"AppData", "Roaming", "Microsoft", "Windows", "Themes", "TranscodedWallpaper"), 200*1024)
	writeFixture(t, filepath.Join(mountPoint, "Users", "Alice", "Pictures", "Wallpapers", "mine.jpg"), 100*1024)
	writeFixture(t, filepath.Join(mountPoint, "Windows", "Web", "Wallpaper", "sys.jpg"), 100*1024)
	writeFixture(t, filepath.Join(mountPoint, "Users", "Public", "shared.jpg"), 100*1024) // skipped

	fakeRun(t)
	result, err := ExtractWallpapers("/dev/sda2")
	if err != nil {
		t.Fatalf("ExtractWallpapers: %v", err)
	}
	if !result.Found {
		t.Fatal("Found = false, want true")
	}
	// Alice: TranscodedWallpaper + Pictures/Wallpapers + system default = 3.
	if result.Count != 3 {
		t.Errorf("Count = %d, want 3", result.Count)
	}
	if result.TotalBytes <= 0 {
		t.Errorf("TotalBytes = %d, want > 0", result.TotalBytes)
	}
	if result.ScratchDir != filepath.Join(scratch, "wallpapers") {
		t.Errorf("ScratchDir = %q", result.ScratchDir)
	}
	for _, name := range []string{"TranscodedWallpaper", "mine.jpg", "sys.jpg"} {
		if _, err := os.Stat(filepath.Join(scratch, "wallpapers", "Alice", name)); err != nil {
			if name == "sys.jpg" {
				if _, err2 := os.Stat(filepath.Join(scratch, "wallpapers", "_windows_defaults", name)); err2 != nil {
					t.Errorf("%s not copied anywhere: %v", name, err2)
				}
				continue
			}
			t.Errorf("%s not copied: %v", name, err)
		}
	}
}

func TestExtractWallpapers_MountFailure(t *testing.T) {
	_, mountPoint := withScratchDirs(t)
	writeFixture(t, filepath.Join(mountPoint, "Users", "Alice", "Documents", "a.txt"), 10)

	fakeRun(t, "mount")
	result, err := ExtractWallpapers("/dev/sda2")
	if err == nil {
		t.Fatal("expected a mount error")
	}
	if result.Found {
		t.Error("Found must stay false when nothing was extracted")
	}
}

// --- CleanupScratch ---

func TestCleanupScratch(t *testing.T) {
	scratch, _ := withScratchDirs(t)
	writeFixture(t, filepath.Join(scratch, "wallpapers", "x.jpg"), 100)
	CleanupScratch()
	if _, err := os.Stat(scratch); !os.IsNotExist(err) {
		t.Errorf("scratch dir should be gone after CleanupScratch, err=%v", err)
	}
}

// --- data.go: categoryPath / streamCategory ---

func TestCategoryPath(t *testing.T) {
	cases := map[string]string{
		"Documents":  "Documents",
		"documents":  "Documents", // case-insensitive
		"Pictures":   "Pictures",
		"Wallpapers": "AppData/Roaming/Microsoft/Windows/Themes",
		"Nonsense":   "",
	}
	for in, want := range cases {
		if got := categoryPath(in); got != want {
			t.Errorf("categoryPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestStreamCategory_CopiesAndCounts(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()
	writeFixture(t, filepath.Join(src, "a.txt"), 100)
	writeFixture(t, filepath.Join(src, "sub", "b.txt"), 50)
	writeFixture(t, filepath.Join(src, ".hidden"), 999)                   // skipped (dotfile)
	writeFixture(t, filepath.Join(src, "$Recycle.Bin", "$Rabc.jpg"), 999) // skipped ($-prefixed)

	bytes, count := streamCategory(src, dst, 10_000)
	if count != 2 {
		t.Errorf("count = %d, want 2", count)
	}
	if bytes != 150 {
		t.Errorf("bytes = %d, want 150", bytes)
	}
	for _, rel := range []string{"a.txt", filepath.Join("sub", "b.txt")} {
		if _, err := os.Stat(filepath.Join(dst, rel)); err != nil {
			t.Errorf("missing copied %s: %v", rel, err)
		}
	}
}

func TestStreamCategory_RespectsBudget(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()
	writeFixture(t, filepath.Join(src, "a.txt"), 100)
	writeFixture(t, filepath.Join(src, "big.bin"), 500) // pushes past budget

	bytes, count := streamCategory(src, dst, 150)
	if bytes > 150 {
		t.Errorf("bytes = %d, want <= 150 (budget)", bytes)
	}
	if count != 1 {
		t.Errorf("count = %d, want 1 (copy stops at the budget)", count)
	}
}

func TestStreamCategory_SkipsHugeFiles(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()
	writeFixture(t, filepath.Join(src, "ok.txt"), 10)
	// Sparse 501MB file: skipped by the >500MB check before any read.
	huge := filepath.Join(src, "huge.bin")
	f, err := os.OpenFile(huge, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(501 * 1024 * 1024); err != nil {
		t.Fatal(err)
	}
	f.Close()

	bytes, count := streamCategory(src, dst, 1<<30)
	if count != 1 || bytes != 10 {
		t.Errorf("count=%d bytes=%d, want 1/10 (huge file skipped)", count, bytes)
	}
}

// --- ExtractData / InjectData ---

func TestExtractData_FullFlow(t *testing.T) {
	scratch, mountPoint := withScratchDirs(t)
	writeFixture(t, filepath.Join(mountPoint, "Users", "Alice", "Documents", "a.txt"), 100)
	writeFixture(t, filepath.Join(mountPoint, "Users", "Alice", "Pictures", "p.png"), 200)
	writeFixture(t, filepath.Join(mountPoint, "Users", "Bob", "Documents", "b.txt"), 50)

	fakeRun(t)
	config := &SlurpConfig{
		SourcePartition: "/dev/sda2",
		Users: []SlurpUserConfig{
			{Name: "Alice", Categories: []string{"Documents", "Pictures", "Missing"}},
			{Name: "Carol", Categories: []string{"Documents"}}, // profile absent -> skipped
		},
	}
	result, err := ExtractData(config)
	if err != nil {
		t.Fatalf("ExtractData: %v", err)
	}
	if !result.Found {
		t.Fatal("Found = false, want true")
	}
	if result.TotalBytes != 300 || result.FileCount != 2 {
		t.Errorf("TotalBytes=%d FileCount=%d, want 300/2", result.TotalBytes, result.FileCount)
	}
	if len(result.Users) != 1 || result.Users[0].Name != "Alice" {
		t.Fatalf("users = %+v, want only Alice", result.Users)
	}
	if len(result.Users[0].Categories) != 2 {
		t.Errorf("Alice categories = %+v, want Documents + Pictures", result.Users[0].Categories)
	}
	if _, err := os.Stat(filepath.Join(scratch, "data", "Alice", "Documents", "a.txt")); err != nil {
		t.Errorf("data not copied to scratch: %v", err)
	}
}

func TestExtractData_InvalidConfig(t *testing.T) {
	if _, err := ExtractData(nil); err == nil {
		t.Error("nil config must error")
	}
	if _, err := ExtractData(&SlurpConfig{SourcePartition: "/dev/sda2"}); err == nil {
		t.Error("config without users must error")
	}
}

func TestExtractData_MountFailure(t *testing.T) {
	_, mountPoint := withScratchDirs(t)
	writeFixture(t, filepath.Join(mountPoint, "Users", "Alice", "Documents", "a.txt"), 10)

	fakeRun(t, "mount")
	_, err := ExtractData(&SlurpConfig{
		SourcePartition: "/dev/sda2",
		Users:           []SlurpUserConfig{{Name: "Alice", Categories: []string{"Documents"}}},
	})
	if err == nil {
		t.Fatal("expected a mount error")
	}
}

func TestInjectData_Success(t *testing.T) {
	target := t.TempDir()
	scratch := t.TempDir()
	writeFixture(t, filepath.Join(scratch, "Alice", "Documents", "a.txt"), 100)
	if err := os.MkdirAll(filepath.Join(target, "var", "home", "bob"), 0o755); err != nil {
		t.Fatal(err)
	}

	calls := fakeRun(t)
	result := &DataSlurpResult{
		Found:      true,
		ScratchDir: scratch,
		TotalBytes: 100,
		Users: []SlurpUserResult{{
			Name:       "Alice",
			Categories: []SlurpCategoryResult{{Name: "Documents", Bytes: 100, Count: 1}},
		}},
	}
	if err := InjectData(target, result, false); err != nil {
		t.Fatalf("InjectData: %v", err)
	}
	// First non-hidden dir under var/home (bob) becomes the user home.
	dst := filepath.Join(target, "var", "home", "bob", "Documents")
	if _, err := os.Stat(dst); err != nil {
		t.Errorf("Documents dir not created under first user home: %v", err)
	}
	joined := strings.Join(calls(), " ")
	if !strings.Contains(joined, "cp -a "+scratch+"/Alice/Documents/."+" "+dst+"/") {
		t.Errorf("expected cp -a into the user home, calls: %s", joined)
	}
	if !strings.Contains(joined, "chown -R 1000:1000") {
		t.Errorf("expected chown of the user home, calls: %s", joined)
	}
}

func TestInjectData_ComposefsNativePath(t *testing.T) {
	target := t.TempDir()
	scratch := t.TempDir()
	writeFixture(t, filepath.Join(scratch, "Alice", "Documents", "a.txt"), 100)

	fakeRun(t)
	result := &DataSlurpResult{
		Found:      true,
		ScratchDir: scratch,
		Users: []SlurpUserResult{{
			Name:       "Alice",
			Categories: []SlurpCategoryResult{{Name: "Documents"}},
		}},
	}
	if err := InjectData(target, result, true); err != nil {
		t.Fatalf("InjectData: %v", err)
	}
	home := filepath.Join(target, "state", "os", "default", "var", "home")
	if _, err := os.Stat(filepath.Join(home, "Documents")); err != nil {
		t.Errorf("composefs-native home path not used: %v", err)
	}
}

func TestInjectWallpapers_Success(t *testing.T) {
	target := t.TempDir()
	scratch := t.TempDir()
	writeFixture(t, filepath.Join(scratch, "x.jpg"), 100)

	calls := fakeRun(t)
	result := &WallpaperResult{Found: true, ScratchDir: scratch, Count: 1, TotalBytes: 100}
	if err := InjectWallpapers(target, result, false); err != nil {
		t.Fatalf("InjectWallpapers: %v", err)
	}
	bgDir := filepath.Join(target, "var", "home", ".windows-wallpapers")
	if _, err := os.Stat(bgDir); err != nil {
		t.Errorf("wallpaper target dir not created: %v", err)
	}
	joined := strings.Join(calls(), " ")
	if !strings.Contains(joined, "cp -a "+scratch+"/. "+bgDir+"/") {
		t.Errorf("expected cp -a from scratch into bgDir, calls: %s", joined)
	}
	if !strings.Contains(joined, "chown -R 1000:1000 "+bgDir) {
		t.Errorf("expected chown of the wallpaper dir, calls: %s", joined)
	}
}

func TestInjectWallpapers_ComposefsNativePath(t *testing.T) {
	target := t.TempDir()
	scratch := t.TempDir()
	writeFixture(t, filepath.Join(scratch, "x.jpg"), 100)

	fakeRun(t)
	result := &WallpaperResult{Found: true, ScratchDir: scratch, Count: 1}
	if err := InjectWallpapers(target, result, true); err != nil {
		t.Fatalf("InjectWallpapers: %v", err)
	}
	bgDir := filepath.Join(target, "state", "os", "default", "var", "home", ".windows-wallpapers")
	if _, err := os.Stat(bgDir); err != nil {
		t.Errorf("composefs-native wallpaper dir not created: %v", err)
	}
}

func TestInjectData_NilOrNotFound(t *testing.T) {
	if err := InjectData("/tmp/t", nil, false); err != nil {
		t.Errorf("nil result: %v", err)
	}
	if err := InjectData("/tmp/t", &DataSlurpResult{Found: false}, false); err != nil {
		t.Errorf("not-found result: %v", err)
	}
}
