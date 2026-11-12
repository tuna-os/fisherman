package diagnostics

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/tuna-os/fisherman/internal/recipe"
)

var now = time.Date(2026, 11, 12, 8, 0, 0, 0, time.UTC)

// recipeJSONFields returns every json field path of t, as RecipeFieldPolicy
// spells them ("user.password", "customMounts[].fstype"). A struct field
// with its own entry in want stops the descent (sourceReuse, slurp).
func recipeJSONFields(t reflect.Type, prefix string, want map[string]string, out map[string]bool) {
	for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice {
		if t.Kind() == reflect.Slice {
			if t.Elem().Kind() != reflect.Struct {
				break
			}
			prefix += "[]"
		}
		t = t.Elem()
	}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "" || name == "-" {
			continue
		}
		path := name
		if prefix != "" {
			path = prefix + "." + name
		}
		ft := f.Type
		for ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		elem := ft
		if elem.Kind() == reflect.Slice {
			elem = elem.Elem()
		}
		if _, own := want[path]; !own && elem.Kind() == reflect.Struct {
			recipeJSONFields(f.Type, path, want, out)
			continue
		}
		out[path] = true
	}
}

// TestRecipeFieldPolicyCoversEveryField: every json field of recipe.Recipe
// has a policy, and every policy names a real field. A new recipe field is
// left out of the bundle until it gets one, and this test says so.
func TestRecipeFieldPolicyCoversEveryField(t *testing.T) {
	fields := map[string]bool{}
	recipeJSONFields(reflect.TypeOf(recipe.Recipe{}), "", RecipeFieldPolicy, fields)
	for f := range fields {
		if _, ok := RecipeFieldPolicy[f]; !ok {
			t.Errorf("recipe field %q has no diagnostics policy (RecipeFieldPolicy, docs/DIAGNOSTICS.md)", f)
		}
	}
	for f, p := range RecipeFieldPolicy {
		if !fields[f] {
			t.Errorf("RecipeFieldPolicy names %q, which recipe.Recipe does not have", f)
		}
		switch p {
		case PolicyValue, PolicyIdentifier, PolicyPresence, PolicySecret:
		default:
			t.Errorf("%s: unknown policy %q", f, p)
		}
	}
}

// TestSecretFieldsOnlyPresence: a secret field is in the summary as a
// boolean, never as a value.
func TestSecretFieldsOnlyPresence(t *testing.T) {
	data := []byte(`{"encryption":{"type":"luks-passphrase","passphrase":"hunter2hunter2"},"user":{"username":"ann","password":"pw-pw-pw-pw"}}`)
	s, ok := summarizeRecipe(data, NewRedactor(true))
	if !ok {
		t.Fatal("recipe not summarized")
	}
	if !s.Encryption.PassphraseSet || s.User == nil || !s.User.PasswordSet {
		t.Errorf("secret presence not recorded: %+v %+v", s.Encryption, s.User)
	}
	out, _ := json.Marshal(s)
	for _, v := range []string{"hunter2", "pw-pw"} {
		if strings.Contains(string(out), v) {
			t.Errorf("summary holds %q: %s", v, out)
		}
	}
}

func TestRecipeUnreadable(t *testing.T) {
	for name, data := range map[string][]byte{
		"not json": []byte("passphrase=hunter2hunter2"),
		"array":    []byte(`["hunter2hunter2"]`),
		"type":     []byte(`{"encryption":{"passphrase":7},"hostname":"hunter2hunter2"}`),
	} {
		b := Build(Inputs{Recipe: data, Root: t.TempDir(), Now: now})
		if b.Recipe != nil || b.Manifest.Sections["recipe"] != StatusUnreadable {
			t.Errorf("%s: recipe = %+v, status %q; want nothing, %q", name, b.Recipe, b.Manifest.Sections["recipe"], StatusUnreadable)
		}
		out, _ := Marshal(b)
		if strings.Contains(string(out), "hunter2") {
			t.Errorf("%s: bundle holds the unreadable recipe's value", name)
		}
	}
}

func TestSectionsNotRequested(t *testing.T) {
	b := Build(Inputs{Root: t.TempDir(), Now: now})
	for _, s := range []string{"recipe", "probe", "progress"} {
		if b.Manifest.Sections[s] != StatusNotRequested {
			t.Errorf("%s: status %q, want %q", s, b.Manifest.Sections[s], StatusNotRequested)
		}
	}
	if b.Recipe != nil || b.Probe != nil || b.Progress != nil {
		t.Error("a section without input has content")
	}
	b = Build(Inputs{Root: t.TempDir(), Now: now, ProbeStatus: StatusTimedOut, RecipeErr: errors.New("x"), ProgressErr: errors.New("y")})
	if b.Manifest.Sections["probe"] != StatusTimedOut || b.Manifest.Sections["recipe"] != StatusUnreadable || b.Manifest.Sections["progress"] != StatusUnreadable {
		t.Errorf("sections = %v", b.Manifest.Sections)
	}
}

func lines(ls ...string) []byte { return []byte(strings.Join(ls, "\n") + "\n") }

// TestProgressOutcomes: the outcome of a failed cleanup, a cancellation, a
// complete install and a cut log.
func TestProgressOutcomes(t *testing.T) {
	step := `{"type":"step","step":3,"total_steps":9,"step_name":"Formatting","step_id":"format","overall_pct":12}`
	for name, c := range map[string]struct {
		log  []byte
		want Outcome
	}{
		"failed cleanup": {lines(step, `{"type":"error","message":"mkfs failed","code":"format_failed","step_id":"format","disk_changed":true,"cleanup":{"status":"failed","message":"umount: target is busy"}}`),
			Outcome{State: "error", Code: "format_failed", StepID: "format", DiskChanged: ptr(true), Cleanup: "failed"}},
		"cancelled": {lines(step, `{"type":"error","message":"cancelled","code":"cancelled","step_id":"format","disk_changed":true,"cleanup":{"status":"ok"}}`),
			Outcome{State: "error", Code: "cancelled", StepID: "format", DiskChanged: ptr(true), Cleanup: "ok"}},
		"timed out cleanup": {lines(step, `{"type":"error","message":"x","code":"luks_failed","cleanup":{"status":"timed_out"}}`),
			Outcome{State: "error", Code: "luks_failed", Cleanup: "timed_out"}},
		"no code":    {lines(`{"type":"error","message":"old fisherman"}`), Outcome{State: "error", Code: "unknown"}},
		"bad code":   {lines(`{"type":"error","message":"x","code":"Not A Code"}`), Outcome{State: "error", Code: Other}},
		"complete":   {lines(step, `{"type":"complete","message":"done","overall_pct":100}`), Outcome{State: "complete"}},
		"unfinished": {lines(step, `{"type":"info","message":"pulling`), Outcome{State: "unfinished"}},
		"empty":      {[]byte{}, Outcome{State: "unfinished"}},
	} {
		s := summarizeProgress(c.log, false, NewRedactor(false))
		if !reflect.DeepEqual(s.Outcome, c.want) {
			t.Errorf("%s: outcome %+v, want %+v", name, s.Outcome, c.want)
		}
	}
}

func ptr[T any](v T) *T { return &v }

// TestProgressMalformedMixed: lines that are not events are counted and
// left out; unknown event types and fields are left out.
func TestProgressMalformedMixed(t *testing.T) {
	log := lines(
		`{"type":"step","step":1,"total_steps":2,"step_id":"partition","new_field":"x-secret-x"}`,
		`[fisherman] version: dev`,
		`{"type":"info","message":`,
		`{"type":"future_event","payload":"x-secret-x"}`,
		`{"type":7}`,
		`["type","error"]`,
		strings.Repeat("x", maxLine+1),
		`{"type":"substep","message":"x-secret-x"}`,
	)
	s := summarizeProgress(log, false, NewRedactor(false))
	if s.Malformed != 4 || s.Oversized != 1 || s.Unknown != 1 {
		t.Errorf("malformed %d oversized %d unknown %d; want 4, 1, 1", s.Malformed, s.Oversized, s.Unknown)
	}
	if len(s.Timeline) != 1 || s.Timeline[0].Messages != 1 {
		t.Errorf("timeline = %+v", s.Timeline)
	}
	out, _ := json.Marshal(s)
	if strings.Contains(string(out), "x-secret-x") || strings.Contains(string(out), "future_event") {
		t.Errorf("summary holds unknown content: %s", out)
	}
}

// TestProgressTruncatedDropsPartialLine: when only the end of a log was
// read, its first, partial line is not decoded.
func TestProgressTruncatedDropsPartialLine(t *testing.T) {
	log := lines(`"step_id":"partition"}`, `{"type":"step","step":2,"total_steps":2,"step_id":"format"}`)
	s := summarizeProgress(log, true, NewRedactor(false))
	if !s.Truncated || s.Malformed != 0 || len(s.Timeline) != 1 || s.Timeline[0].StepID != "format" {
		t.Errorf("summary = %+v", s)
	}
}

// TestHugeLogBounded: a log with many events and long messages gives a
// bundle within MaxBundleBytes and a timeline within MaxTimeline.
func TestHugeLogBounded(t *testing.T) {
	var ls []string
	long := strings.Repeat("é", maxLine/20)
	const n = MaxTimeline/2 + 100
	for i := 0; i < n; i++ {
		ls = append(ls, fmt.Sprintf(`{"type":"step","step":%d,"total_steps":9,"step_id":"bootc_install"}`, i))
		ls = append(ls, fmt.Sprintf(`{"type":"error","message":%q,"code":"pull_failed","cleanup":{"status":"ok","message":%q}}`, long, long))
	}
	b := Build(Inputs{Progress: lines(ls...), Root: t.TempDir(), Now: now})
	if n := len(b.Progress.Timeline); n > MaxTimeline {
		t.Errorf("timeline has %d events, bound %d", n, MaxTimeline)
	}
	out, err := Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) > MaxBundleBytes {
		t.Errorf("bundle is %d bytes, bound %d", len(out), MaxBundleBytes)
	}
	var back Bundle
	if err := json.Unmarshal(out, &back); err != nil {
		t.Fatal(err)
	}
	if back.Progress.Dropped == 0 || back.Progress.Dropped+len(back.Progress.Timeline) != 2*n {
		t.Errorf("dropped %d + kept %d, want %d", back.Progress.Dropped, len(back.Progress.Timeline), 2*n)
	}
	for _, e := range back.Progress.Timeline {
		if len(e.Message) > maxMessage {
			t.Fatalf("message of %d bytes, bound %d", len(e.Message), maxMessage)
		}
	}
}

var nameRe = regexp.MustCompile(`^fisherman-diagnostics-20261112T080000Z-[0-9a-f]{8}\.json$`)

func TestWriteModeAndName(t *testing.T) {
	dir := t.TempDir()
	path, err := Write(dir, now, []byte("{}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(path) != dir || !nameRe.MatchString(filepath.Base(path)) {
		t.Errorf("path %q", path)
	}
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 || !fi.Mode().IsRegular() {
		t.Errorf("mode %v, want a regular 0600 file", fi.Mode())
	}
}

// TestWriteCollisionAndSymlink: a taken name, a file or a symlink (even a
// dangling one), is never opened; the next random name is used.
func TestWriteCollisionAndSymlink(t *testing.T) {
	dir := t.TempDir()
	victim := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(victim, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	suffixes := []string{"aaaaaaaa", "bbbbbbbb", "cccccccc"}
	if err := os.Symlink(victim, filepath.Join(dir, FileName(now, "aaaaaaaa"))); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "missing"), filepath.Join(dir, FileName(now, "bbbbbbbb"))); err != nil {
		t.Fatal(err)
	}
	old := randSuffix
	defer func() { randSuffix = old }()
	i := 0
	randSuffix = func() (string, error) { s := suffixes[i]; i++; return s, nil }

	path, err := Write(dir, now, []byte("{}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != FileName(now, "cccccccc") {
		t.Errorf("path %q, want the third name", path)
	}
	if b, _ := os.ReadFile(victim); string(b) != "keep" {
		t.Errorf("symlink target changed: %q", b)
	}
	if _, err := os.Stat(filepath.Join(dir, "missing")); !os.IsNotExist(err) {
		t.Error("dangling symlink was followed")
	}

	// Every name taken: an error, nothing written.
	randSuffix = func() (string, error) { return "aaaaaaaa", nil }
	if _, err := Write(dir, now, []byte("{}\n")); err == nil {
		t.Error("Write succeeded with every name taken")
	}
}

func TestWriteRefusesSymlinkOrFileDir(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{link, file, filepath.Join(real, "absent")} {
		if _, err := Write(dir, now, []byte("{}")); !errors.Is(err, ErrOutputDir) {
			t.Errorf("Write(%s) = %v, want ErrOutputDir", dir, err)
		}
	}
	if ents, _ := os.ReadDir(real); len(ents) != 0 {
		t.Errorf("wrote through the symlink: %v", ents)
	}
}

// TestWriteFullDiskLeavesNothing: a write error (a full disk) removes the
// partial file.
func TestWriteFullDiskLeavesNothing(t *testing.T) {
	dir := t.TempDir()
	old := writeAll
	defer func() { writeAll = old }()
	writeAll = func(f *os.File, data []byte) error {
		_, _ = f.Write(data[:len(data)/2])
		return syscall.ENOSPC
	}
	if _, err := Write(dir, now, []byte(`{"half":"of a bundle"}`)); !errors.Is(err, syscall.ENOSPC) {
		t.Fatalf("err = %v, want ENOSPC", err)
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 0 {
		t.Errorf("partial output left: %v", ents)
	}
}

func TestMountsFixture(t *testing.T) {
	root := sentinelRoot(t)
	m := hostMounts(root, DefaultTargetMount, DefaultLuksMapper, NewRedactor(true))
	var got []string
	for _, e := range m.Entries {
		got = append(got, e.Under+":"+e.Path+":"+e.Fstype)
	}
	want := []string{"target:/:xfs", "target:/boot:ext4", "scratch:/:tmpfs"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("entries %v, want %v", got, want)
	}
	if !m.Mapper || m.OtherMappers != 1 {
		t.Errorf("mapper %v other %d; want true, 1", m.Mapper, m.OtherMappers)
	}
	if !m.Entries[1].ReadOnly || m.Entries[0].Source != "/dev/mapper/fisherman-root" {
		t.Errorf("entries %+v", m.Entries)
	}
	// A custom target and mapper (the recipe's targetMount, luksMapperName).
	m = hostMounts(root, "/mnt/other", "fisherman-root2", NewRedactor(false))
	if len(m.Entries) != 1 || m.Entries[0].Under != "scratch" || m.Mapper || m.OtherMappers != 2 {
		t.Errorf("custom target: %+v", m)
	}
}

func TestUnescapeMountinfo(t *testing.T) {
	for in, want := range map[string]string{`/mnt/a\040b`: "/mnt/a b", `/x\134y`: `/x\y`, `/end\04`: `/end\04`, "/plain": "/plain"} {
		if got := unescapeMountinfo(in); got != want {
			t.Errorf("unescape(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMessageRedaction(t *testing.T) {
	rd := NewRedactor(false)
	rd.AddSecret("correct horse battery")
	rd.AddSecret("abc") // too short: ignored, like progress.RegisterSecret
	rd.Ident("host", "myhost")
	for in, want := range map[string]string{
		"open with correct horse battery failed": "open with [redacted] failed",
		"cut correct horse b…":                   "cut [redacted]…",
		"ends correct hor":                       "ends [redacted]",
		"abc stays":                              "abc stays",
		"mount /dev/sdb2 on myhost":              "mount device-1 on host-1",
		"pull docker://u:p@reg.example/x":        "pull [redacted]reg.example/x",
		"Authorization: Bearer abc.def":          "[redacted]",
		"wifi psk=s3cr3t ok":                     "wifi [redacted] ok",
		"ssh-key -----BEGIN OPENSSH PRIVATE KEY-----\nAAAA\n-----END OPENSSH PRIVATE KEY----- done": "ssh-key [redacted] done",
		"hash $6$salt$abcdef: bad": "hash [redacted]: bad",
	} {
		if got := rd.Message(in, 2048); got != want {
			t.Errorf("Message(%q) = %q, want %q", in, got, want)
		}
	}
	if got := rd.Message(strings.Repeat("a", 100), 10); len(got) > 10 {
		t.Errorf("bound: %d bytes", len(got))
	}
}

func TestImageRef(t *testing.T) {
	for in, want := range map[string]string{
		"ghcr.io/ublue-os/bluefin:stable":                        "ghcr.io/ublue-os/bluefin:stable",
		"docker://quay.io/x/y@sha256:" + strings.Repeat("a", 64): "docker://quay.io/x/y@sha256:" + strings.Repeat("a", 64),
		"containers-storage:localhost/x:1":                       "containers-storage:localhost/x:1",
		"oci:/var/lib/store/x":                                   Omitted,
		"docker://user:pass@reg.example/x":                       Omitted,
		"user@reg.example/x":                                     Omitted,
		"x y":                                                    Omitted,
		"":                                                       "",
	} {
		if got := imageRef(in); got != want {
			t.Errorf("imageRef(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestDiagnosticsDoc holds docs/DIAGNOSTICS.md to the code: the recipe
// field table lists every RecipeFieldPolicy entry with its policy, and the
// omitted list names every AlwaysOmitted item.
func TestDiagnosticsDoc(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "docs", "DIAGNOSTICS.md"))
	if err != nil {
		t.Fatal(err)
	}
	doc := string(b)
	row := regexp.MustCompile("(?m)^\\| `([A-Za-z\\[\\].]+)` \\| ([a-z]+) \\|")
	got := map[string]string{}
	for _, m := range row.FindAllStringSubmatch(doc, -1) {
		got[m[1]] = m[2]
	}
	var keys []string
	for k := range RecipeFieldPolicy {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if got[k] != RecipeFieldPolicy[k] {
			t.Errorf("DIAGNOSTICS.md: `%s` is %q, the code says %q", k, got[k], RecipeFieldPolicy[k])
		}
	}
	for k := range got {
		if _, ok := RecipeFieldPolicy[k]; !ok {
			t.Errorf("DIAGNOSTICS.md lists `%s`, which has no policy", k)
		}
	}
	for _, o := range AlwaysOmitted {
		if !strings.Contains(doc, "- "+o+"\n") {
			t.Errorf("DIAGNOSTICS.md does not list %q as omitted", o)
		}
	}
	for _, s := range []string{StatusIncluded, StatusNotRequested, StatusUnreadable, StatusTimedOut} {
		if !strings.Contains(doc, "`"+s+"`") {
			t.Errorf("DIAGNOSTICS.md does not name status %q", s)
		}
	}
}

// TestMarshalShrinksToBound: a bundle over MaxBundleBytes loses its oldest
// timeline events until it fits.
func TestMarshalShrinksToBound(t *testing.T) {
	msg := strings.Repeat("m", maxMessage)
	p := &ProgressSummary{Counts: map[string]int{}}
	for i := 0; i < 4000; i++ {
		p.Timeline = append(p.Timeline, Event{Type: "error", Message: msg, Cleanup: &CleanupShape{Status: "ok", Message: msg}})
	}
	out, err := Marshal(Bundle{Progress: p})
	if err != nil {
		t.Fatal(err)
	}
	var back Bundle
	if err := json.Unmarshal(out, &back); err != nil {
		t.Fatal(err)
	}
	if len(out) > MaxBundleBytes || back.Progress.Dropped == 0 || back.Progress.Dropped+len(back.Progress.Timeline) != 4000 {
		t.Errorf("%d bytes, dropped %d, kept %d", len(out), back.Progress.Dropped, len(back.Progress.Timeline))
	}
}
