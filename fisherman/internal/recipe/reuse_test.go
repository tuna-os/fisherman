package recipe_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/tuna-os/fisherman/internal/recipe"
)

const reuseDigest = "sha256:1111111111111111111111111111111111111111111111111111111111111111"

func reuseRecipe() *recipe.Recipe {
	return &recipe.Recipe{
		Filesystem:   "xfs",
		TargetImgref: "ghcr.io/example/os:stable",
		SourceReuse:  &recipe.SourceReuse{Store: "bootc", Digest: reuseDigest, Platform: "x86_64"},
		Experimental: []string{recipe.ExperimentalBootcStoreReuse},
	}
}

func codes(ps []recipe.Problem) []string {
	out := []string{}
	for _, p := range ps {
		out = append(out, p.Field+":"+p.Code)
	}
	return out
}

func TestCheckSourceReuse(t *testing.T) {
	cases := []struct {
		name string
		edit func(r *recipe.Recipe)
		want []string
	}{
		{"valid", func(*recipe.Recipe) {}, []string{}},
		{"absent field is never checked", func(r *recipe.Recipe) {
			r.SourceReuse, r.Image, r.TargetImgref, r.Experimental = nil, "x", "", nil
		}, []string{}},
		{"no opt-in", func(r *recipe.Recipe) { r.Experimental = nil },
			[]string{"sourceReuse:source_reuse_experimental"}},
		{"zfs opt-in is not this one", func(r *recipe.Recipe) { r.Experimental = []string{"zfs"} },
			[]string{"sourceReuse:source_reuse_experimental"}},
		{"store", func(r *recipe.Recipe) { r.SourceReuse.Store = "podman" },
			[]string{"sourceReuse.store:source_reuse_store_unsupported"}},
		{"store empty", func(r *recipe.Recipe) { r.SourceReuse.Store = "" },
			[]string{"sourceReuse.store:source_reuse_store_unsupported"}},
		{"digest is a tag", func(r *recipe.Recipe) { r.SourceReuse.Digest = "latest" },
			[]string{"sourceReuse.digest:source_reuse_digest_invalid"}},
		{"digest upper case", func(r *recipe.Recipe) { r.SourceReuse.Digest = strings.ToUpper(reuseDigest) },
			[]string{"sourceReuse.digest:source_reuse_digest_invalid"}},
		{"digest short", func(r *recipe.Recipe) { r.SourceReuse.Digest = reuseDigest[:20] },
			[]string{"sourceReuse.digest:source_reuse_digest_invalid"}},
		{"platform missing", func(r *recipe.Recipe) { r.SourceReuse.Platform = "" },
			[]string{"sourceReuse.platform:source_reuse_platform_invalid"}},
		{"platform with slash", func(r *recipe.Recipe) { r.SourceReuse.Platform = "linux/amd64" },
			[]string{"sourceReuse.platform:source_reuse_platform_invalid"}},
		{"image as well", func(r *recipe.Recipe) { r.Image = "ghcr.io/example/os:stable" },
			[]string{"image:source_reuse_image_conflict"}},
		{"no update ref", func(r *recipe.Recipe) { r.TargetImgref = "" },
			[]string{"targetImgref:source_reuse_target_imgref_required"}},
		{"update ref is local storage", func(r *recipe.Recipe) { r.TargetImgref = "containers-storage:localhost/fisherman-reuse:x" },
			[]string{"targetImgref:source_reuse_target_imgref_invalid"}},
		{"update ref is localhost", func(r *recipe.Recipe) { r.TargetImgref = "localhost/os:1" },
			[]string{"targetImgref:source_reuse_target_imgref_invalid"}},
		{"update ref is localhost with port", func(r *recipe.Recipe) { r.TargetImgref = "localhost:5000/os:1" },
			[]string{"targetImgref:source_reuse_target_imgref_invalid"}},
		{"update ref is oci", func(r *recipe.Recipe) { r.TargetImgref = "oci:/srv/os" },
			[]string{"targetImgref:source_reuse_target_imgref_invalid"}},
		{"update ref with credentials", func(r *recipe.Recipe) { r.TargetImgref = "user:token@ghcr.io/example/os:1" },
			[]string{"targetImgref:source_reuse_target_imgref_invalid"}},
		{"update ref with spaces", func(r *recipe.Recipe) { r.TargetImgref = "ghcr.io/example/os:1 --x" },
			[]string{"targetImgref:source_reuse_target_imgref_invalid"}},
		{"update ref docker:// is fine", func(r *recipe.Recipe) { r.TargetImgref = "docker://ghcr.io/example/os:1" },
			[]string{}},
		{"update ref pinned is fine", func(r *recipe.Recipe) { r.TargetImgref = "ghcr.io/example/os@" + reuseDigest },
			[]string{}},
		{"composefs", func(r *recipe.Recipe) { r.ComposeFsBackend = true },
			[]string{"sourceReuse:source_reuse_backend_unsupported"}},
		{"zfs forces composefs", func(r *recipe.Recipe) {
			r.Filesystem = "zfs"
			r.Experimental = append(r.Experimental, "zfs")
		}, []string{"sourceReuse:source_reuse_backend_unsupported"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := reuseRecipe()
			c.edit(r)
			if got := codes(recipe.CheckSourceReuse(r)); !reflect.DeepEqual(got, c.want) {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
}

func TestCheckSourceReusePartial(t *testing.T) {
	r := reuseRecipe()
	r.TargetImgref = ""
	r.Image = "x"
	// A page with only the reuse choice is not told about fields it lacks.
	if got := codes(recipe.CheckSourceReusePartial(r, false, false)); len(got) != 0 {
		t.Errorf("got %v, want none", got)
	}
	want := []string{"image:source_reuse_image_conflict", "targetImgref:source_reuse_target_imgref_required"}
	if got := codes(recipe.CheckSourceReusePartial(r, true, true)); !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestProblems_SourceReuse(t *testing.T) {
	r := reuseRecipe()
	r.Disk, r.Hostname = "/nonexistent-disk", "host"
	r.Experimental = nil
	found := false
	for _, p := range r.Problems() {
		found = found || p.Code == recipe.CodeSourceReuseExperimental
	}
	if !found {
		t.Error("Problems does not run the sourceReuse rules")
	}
	// An empty image is the reuse's own, not image_required.
	if err := r.ValidateImage(false); err != nil {
		t.Errorf("ValidateImage with sourceReuse: %v", err)
	}
	r.SourceReuse = nil
	if err := r.ValidateImage(false); err == nil {
		t.Error("ValidateImage without sourceReuse off live media must still refuse an empty image")
	}
}

func TestSourceReuse_StrictParse(t *testing.T) {
	ok := `{"sourceReuse":{"store":"bootc","digest":"` + reuseDigest + `","platform":"x86_64"}}`
	if _, err := recipe.Parse([]byte(ok)); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	bad := `{"sourceReuse":{"store":"bootc","digest":"` + reuseDigest + `","platform":"x86_64","pull":true}}`
	ps := recipe.UnknownFields([]byte(bad))
	if len(ps) != 1 || ps[0].Field != "sourceReuse.pull" {
		t.Fatalf("unknown sourceReuse key: %+v", ps)
	}
	// Absent stays absent: the field is never a default.
	r, err := recipe.Parse([]byte(`{"image":"x"}`))
	if err != nil || r.SourceReuse != nil {
		t.Fatalf("absent sourceReuse parsed as %+v (%v)", r, err)
	}
	b, _ := json.Marshal(r)
	if strings.Contains(string(b), "sourceReuse") {
		t.Errorf("an absent sourceReuse is written back: %s", b)
	}
}

func TestExperimental_AcceptsBootcStoreReuse(t *testing.T) {
	if got := recipe.CheckExperimental([]string{"zfs", recipe.ExperimentalBootcStoreReuse}); len(got) != 0 {
		t.Errorf("CheckExperimental refused a known entry: %+v", got)
	}
	got := recipe.CheckExperimental([]string{"unified"})
	if len(got) != 1 || !strings.Contains(got[0].Message, `"bootc_store_reuse"`) {
		t.Errorf("message does not list the accepted values: %+v", got)
	}
}

// TestSourceReuseCodesDocumented: every source_reuse_* code is in the
// validate contract and in the feature's own document.
func TestSourceReuseCodesDocumented(t *testing.T) {
	src, err := os.ReadFile("reuse.go")
	if err != nil {
		t.Fatal(err)
	}
	codes := regexp.MustCompile(`"(source_reuse_[a-z_]+)"`).FindAllStringSubmatch(string(src), -1)
	if len(codes) < 20 {
		t.Fatalf("found only %d codes", len(codes))
	}
	for _, doc := range []string{"VALIDATE.md", "SOURCE-REUSE.md"} {
		b, err := os.ReadFile(filepath.Join("..", "..", "..", "docs", doc))
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range codes {
			if !strings.Contains(string(b), "`"+c[1]+"`") {
				t.Errorf("docs/%s does not document %s", doc, c[1])
			}
		}
	}
}
