package main

// Field-mapping coverage (#289): a recipe field that is parsed but never
// reaches the install is a silent drop. The caller sets it, validate
// accepts it, and the installed system does not have it.
//
// recipeFieldUses is the explicit table: every field of recipe.Recipe,
// nested ones included, by its JSON path, with the non-test files that
// pass it on to the install. A field that only accepts or refuses the
// recipe (a gate) says so and why. TestRecipeFieldsHaveConsumers fails
// when a field is added to recipe.Recipe without an entry, when an entry
// names a field that is gone, and when a listed file no longer reads the
// field. TestBootcOptionsProjection does the same for the projection
// into install.Options (buildBootcInstallOptions).

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/tuna-os/fisherman/internal/install"
	"github.com/tuna-os/fisherman/internal/recipe"
)

// fieldUse says where the install reads a recipe field. files are paths
// relative to the fisherman module root (this module's go.mod directory).
type fieldUse struct {
	files []string
	// gate is set for a field that decides only whether the recipe is
	// accepted: why nothing else needs it.
	gate string
}

const mainGo = "cmd/fisherman/main.go"

var recipeFieldUses = map[string]fieldUse{
	"disk":            {files: []string{mainGo}},
	"filesystem":      {files: []string{mainGo, "internal/validation/chain.go"}},
	"btrfsSubvolumes": {files: []string{mainGo}},
	"encryption":      {files: []string{mainGo}},
	"encryption.type": {files: []string{mainGo}},
	// Also registered as a secret (progress.RegisterSecret).
	"encryption.passphrase": {files: []string{mainGo}},
	"image":                 {files: []string{mainGo, "cmd/fisherman/host_recipe.go"}},
	"targetImgref":          {files: []string{mainGo, "cmd/fisherman/reuse.go"}},
	"sourceReuse":           {files: []string{mainGo, "cmd/fisherman/reuse.go"}},
	"sourceReuse.store": {files: []string{"internal/recipe/reuse.go"},
		gate: `the only accepted value is "bootc", the store the reuse path always reads`},
	"sourceReuse.digest":   {files: []string{"cmd/fisherman/reuse.go"}},
	"sourceReuse.platform": {files: []string{"cmd/fisherman/reuse.go"}},
	"selinuxDisabled":      {files: []string{mainGo}},
	"unifiedStorage": {files: []string{mainGo, "internal/recipe/recipe.go"},
		gate: "true is refused (unified_storage_unsupported); the projection still carries it"},
	"composeFsBackend": {files: []string{mainGo}},
	"genericImage":     {files: []string{mainGo}},
	"zfsPoolName":      {files: []string{mainGo}},
	"bootloader":       {files: []string{mainGo}},
	"imageType": {files: []string{"internal/recipe/recipe.go"},
		gate: `only "bootc" is accepted; "ostree" is refused until it is implemented`},
	"flatpakVarPath":           {files: []string{mainGo}},
	"hostname":                 {files: []string{mainGo}},
	"flatpaks":                 {files: []string{mainGo}},
	"user":                     {files: []string{mainGo}},
	"user.username":            {files: []string{mainGo}},
	"user.fullname":            {files: []string{mainGo}},
	"user.password":            {files: []string{mainGo}},
	"user.groups":              {files: []string{mainGo}},
	"customMounts":             {files: []string{mainGo, "cmd/fisherman/mounts.go"}},
	"customMounts[].partition": {files: []string{mainGo, "cmd/fisherman/mounts.go"}},
	"customMounts[].target":    {files: []string{mainGo, "cmd/fisherman/mounts.go"}},
	"customMounts[].fstype":    {files: []string{mainGo, "cmd/fisherman/mounts.go"}},
	"varDisk":                  {files: []string{mainGo, "cmd/fisherman/mounts.go"}},
	"varDisk.disk":             {files: []string{mainGo, "cmd/fisherman/mounts.go"}},
	"varDisk.keepExisting":     {files: []string{"internal/recipe/state.go", "cmd/fisherman/mounts.go"}},
	"additionalImageStores":    {files: []string{mainGo, "cmd/fisherman/host_recipe.go"}},
	"slurpWallpapers":          {files: []string{mainGo}},
	"slurp":                    {files: []string{mainGo}},
	"slurp.sourcePartition":    {files: []string{mainGo}},
	"slurp.users":              {files: []string{mainGo}},
	"slurp.users[].name":       {files: []string{mainGo}},
	"slurp.users[].categories": {files: []string{mainGo}},
	"targetMount":              {files: []string{mainGo}},
	"luksMapperName":           {files: []string{mainGo}},
	"distroID":                 {files: []string{mainGo}},
	"brewTap":                  {files: []string{mainGo}},
	"override": {files: []string{"internal/recipe/recipe.go"},
		gate: "accepts a failing hardware-policy check (too_small, uefi) in CheckTarget"},
	"copyNetworkProfiles":   {files: []string{"cmd/fisherman/hostdata.go"}},
	"copyBluetoothPairings": {files: []string{"cmd/fisherman/hostdata.go"}},
	"extras":                {files: []string{"cmd/fisherman/extras.go"}},
	"extras.printServices":  {files: []string{mainGo, "cmd/fisherman/extras.go"}},
	"extras.oemSetup":       {files: []string{mainGo, "cmd/fisherman/extras.go"}},
	"extras.audioNames":     {files: []string{mainGo, "cmd/fisherman/extras.go"}},
	"extras.bootSplash":     {files: []string{mainGo, "cmd/fisherman/extras.go"}},
	"experimental": {files: []string{"internal/recipe/recipe.go"},
		gate: "the opt-in an experimental feature (zfs, bootc-store reuse) needs to be accepted"},
	"setBootNext":      {files: []string{"cmd/fisherman/bootnext.go"}},
	"locale":           {files: []string{"cmd/fisherman/locale.go"}},
	"keyboard":         {files: []string{"cmd/fisherman/locale.go"}},
	"keyboard.layout":  {files: []string{"cmd/fisherman/locale.go"}},
	"keyboard.variant": {files: []string{"cmd/fisherman/locale.go"}},
	"keyboard.model":   {files: []string{"cmd/fisherman/locale.go"}},
	"timezone":         {files: []string{"cmd/fisherman/locale.go"}},
}

// recipeFields walks t and returns every JSON path with the Go field name
// it decodes into. Pointers are followed; a slice of structs adds "[]".
func recipeFields(t reflect.Type, prefix string, out map[string]string) {
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := strings.Split(f.Tag.Get("json"), ",")[0]
		if tag == "-" || !f.IsExported() {
			continue
		}
		if tag == "" {
			tag = f.Name
		}
		path := prefix + tag
		out[path] = f.Name
		ft := f.Type
		for ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		switch {
		case ft.Kind() == reflect.Struct:
			recipeFields(ft, path+".", out)
		case ft.Kind() == reflect.Slice && ft.Elem().Kind() == reflect.Struct:
			recipeFields(ft.Elem(), path+"[].", out)
		}
	}
}

// selectorNames returns every selector name (x.Name) in a Go file.
func selectorNames(t *testing.T, path string) map[string]bool {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	names := map[string]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok {
			names[sel.Sel.Name] = true
		}
		return true
	})
	return names
}

func TestRecipeFieldsHaveConsumers(t *testing.T) {
	fields := map[string]string{}
	recipeFields(reflect.TypeOf(recipe.Recipe{}), "", fields)

	var paths []string
	for p := range fields {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		if _, ok := recipeFieldUses[p]; !ok {
			t.Errorf("recipe field %q (%s) is parsed, and recipeFieldUses names nothing that passes it to the install: "+
				"wire it through, then list the file that reads it (or say why it is only a gate)", p, fields[p])
		}
	}
	for p := range recipeFieldUses {
		if _, ok := fields[p]; !ok {
			t.Errorf("recipeFieldUses lists %q, which recipe.Recipe no longer has", p)
		}
	}

	root := filepath.Join("..", "..") // the module root, from cmd/fisherman
	cache := map[string]map[string]bool{}
	for _, p := range paths {
		use, ok := recipeFieldUses[p]
		if !ok {
			continue
		}
		if len(use.files) == 0 {
			t.Errorf("%s: no file listed", p)
		}
		for _, file := range use.files {
			if strings.HasSuffix(file, "_test.go") {
				t.Errorf("%s: %s is a test; list the file that passes the field on", p, file)
				continue
			}
			full := filepath.Join(root, file)
			if _, err := os.Stat(full); err != nil {
				t.Errorf("%s: %v", p, err)
				continue
			}
			if cache[file] == nil {
				cache[file] = selectorNames(t, full)
			}
			if !cache[file][fields[p]] {
				t.Errorf("%s: %s no longer reads .%s; the field may be dropped on the way to the install", p, file, fields[p])
			}
		}
		// A gate is read where the recipe is judged; the others must reach
		// the install path, not only a rule.
		if use.gate == "" {
			for _, file := range use.files {
				if strings.HasPrefix(file, "internal/recipe/") && file != "internal/recipe/state.go" {
					t.Errorf("%s: %s is a rule, not a consumer; list where the install reads it, or set gate", p, file)
				}
			}
		}
	}
}

// optionsSources says where each install.Options field comes from: a
// recipe field (its JSON path, top level), an argument of
// buildBootcInstallOptions ("arg"), or a step after the projection
// ("later: ..."), which the projection must leave zero.
var optionsSources = map[string]string{
	"SourceImgref":          "arg",
	"TargetImgref":          "arg",
	"SelinuxDisabled":       "selinuxDisabled",
	"SelinuxShimPath":       "later: main() sets it from prepareSelinuxWorkaround",
	"UnifiedStorage":        "unifiedStorage",
	"ComposeFsBackend":      "composeFsBackend",
	"GenericImage":          "genericImage",
	"Bootloader":            "bootloader",
	"Target":                "arg",
	"ScratchDir":            "arg",
	"NeedsPull":             "arg",
	"LayerCount":            "arg",
	"AdditionalImageStores": "arg", // additionalImageStores, or the discovered stores
	"KernelArguments":       "filesystem",
	"ComposeFsOCIPath":      "later: install.BootcInstall sets the container-side OCI path",
	"RunInSourceContainer":  "later: reuseInstallOptions sets it for a sourceReuse install",
	"directLocalSource":     "later: install.BootcInstall sets it for a local containers-storage source",
	"SourceRoot":            "later: main() sets it from establishSourceRoot (#363)",
}

func TestBootcOptionsProjection(t *testing.T) {
	ot := reflect.TypeOf(install.Options{})
	for i := 0; i < ot.NumField(); i++ {
		if _, ok := optionsSources[ot.Field(i).Name]; !ok {
			t.Errorf("install.Options.%s has no entry in optionsSources: say where it comes from and set it in buildBootcInstallOptions", ot.Field(i).Name)
		}
	}
	for name := range optionsSources {
		if _, ok := ot.FieldByName(name); !ok {
			t.Errorf("optionsSources lists %s, which install.Options no longer has", name)
		}
	}

	full := func() *recipe.Recipe {
		return &recipe.Recipe{
			Filesystem: "btrfs", BtrfsSubvolumes: true, SelinuxDisabled: true, UnifiedStorage: true,
			ComposeFsBackend: true, GenericImage: true, Bootloader: "systemd",
		}
	}
	project := func(r *recipe.Recipe) reflect.Value {
		return reflect.ValueOf(buildBootcInstallOptions(r, "oci:/src", "ghcr.io/example/os:stable",
			"/mnt/target", "/var/fisherman-tmp", []string{"/store"}, true, 7))
	}
	opts := project(full())
	jsonField := map[string]string{}
	rt := reflect.TypeOf(recipe.Recipe{})
	for i := 0; i < rt.NumField(); i++ {
		jsonField[strings.Split(rt.Field(i).Tag.Get("json"), ",")[0]] = rt.Field(i).Name
	}
	for name, src := range optionsSources {
		v := opts.FieldByName(name)
		if !v.IsValid() {
			continue
		}
		if strings.HasPrefix(src, "later:") {
			if !v.IsZero() {
				t.Errorf("Options.%s is set after the projection (%s), but the projection set it", name, src)
			}
			continue
		}
		if v.IsZero() {
			t.Errorf("Options.%s is zero for a recipe and arguments that set it: the projection drops it", name)
			continue
		}
		if src == "arg" {
			continue
		}
		goName, ok := jsonField[src]
		if !ok {
			t.Errorf("Options.%s: source %q is not a top-level recipe field", name, src)
			continue
		}
		// Zero the source field: the option must follow it.
		r := full()
		f := reflect.ValueOf(r).Elem().FieldByName(goName)
		f.Set(reflect.Zero(f.Type()))
		if got := project(r).FieldByName(name); !got.IsZero() {
			t.Errorf("Options.%s = %v with recipe %s unset: it does not come from %s", name, got, src, src)
		}
	}
}
