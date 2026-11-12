package main

// The step contract (#316): the step total an install announces up front
// is the number of steps it then announces, in the planned order, for
// every recipe variant.
//
// main() cannot run an install in a test: it partitions, formats and
// mounts as root. So TestMainAnnouncesThePlan replays main() instead. It
// parses main.go, walks main()'s body (with the body of each step phase
// main() calls, such as diskPhase, spliced in; see stepPhases) and follows every `if` that has a
// seq.begin call inside, deciding the branch with the recipe's own
// methods (r.IsManualLayout(), r.EncryptsRoot(), ...), the way main()
// decides it. A step inside any other construct (a loop, a switch, a
// closure) or under any other condition fails the test, so a new step or
// a new condition cannot get past it unmodelled.

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/tuna-os/fisherman/internal/recipe"
)

// stepConsts maps each step constant in steps.go to its name.
func stepConsts(t *testing.T) map[string]string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "steps.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, d := range f.Decls {
		g, ok := d.(*ast.GenDecl)
		if !ok || g.Tok != token.CONST {
			continue
		}
		for _, spec := range g.Specs {
			vs := spec.(*ast.ValueSpec)
			for i, id := range vs.Names {
				if !strings.HasPrefix(id.Name, "step") || i >= len(vs.Values) {
					continue
				}
				lit, ok := vs.Values[i].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					t.Fatalf("step constant %s is not a string literal", id.Name)
				}
				out[id.Name], _ = strconv.Unquote(lit.Value)
			}
		}
	}
	return out
}

// stepPhases are the functions main() hands a run of its steps to, by the
// file that declares them (#313). main() calls each one exactly once, as a
// statement of its own body, passing its own r and seq; mainBody splices
// the phase's body in at that call, so the replay below, and every test
// that reads main()'s steps, sees the install as one sequence still.
var stepPhases = map[string]string{
	"diskPhase":    "disk_phase.go",
	"installPhase": "install_phase.go",
	"postPhase":    "post_phase.go",
}

// phaseCall is the name of the step phase a statement of main() calls
// (`x, err := diskPhase(r, seq, ...)` or `diskPhase(r, seq, ...)`), or "".
func phaseCall(s ast.Stmt) (string, *ast.CallExpr) {
	var e ast.Expr
	switch s := s.(type) {
	case *ast.AssignStmt:
		if len(s.Rhs) == 1 {
			e = s.Rhs[0]
		}
	case *ast.ExprStmt:
		e = s.X
	}
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return "", nil
	}
	if id, ok := call.Fun.(*ast.Ident); ok && stepPhases[id.Name] != "" {
		return id.Name, call
	}
	return "", nil
}

// mainBody returns the body of main() in main.go, with the body of each
// step phase spliced in where main() calls it.
func mainBody(t *testing.T) (*ast.BlockStmt, *token.FileSet) {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "main.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var main *ast.FuncDecl
	for _, d := range f.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok && fn.Name.Name == "main" && fn.Recv == nil {
			main = fn
		}
	}
	if main == nil {
		t.Fatal("main.go has no func main")
	}

	phases := map[string]*ast.FuncDecl{}
	for name, file := range stepPhases {
		pf, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range pf.Decls {
			if fn, ok := d.(*ast.FuncDecl); ok && fn.Name.Name == name && fn.Recv == nil {
				phases[name] = fn
			}
		}
		fn := phases[name]
		if fn == nil {
			t.Fatalf("%s declares no func %s", file, name)
		}
		// The replay decides the phase's branches with r's methods, so the
		// phase must call its recipe r and its sequencer seq.
		var params []string
		for _, fld := range fn.Type.Params.List {
			for _, id := range fld.Names {
				params = append(params, id.Name)
			}
		}
		if len(params) < 2 || params[0] != "r" || params[1] != "seq" {
			t.Fatalf("%s: the first two parameters of %s are %v, want r and seq", file, name, params)
		}
	}

	// Every call of a phase in main() must be one of its top-level
	// statements, so splicing it in keeps the order and the conditions.
	nested := map[string]int{}
	ast.Inspect(main.Body, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			if id, ok := call.Fun.(*ast.Ident); ok && stepPhases[id.Name] != "" {
				nested[id.Name]++
			}
		}
		return true
	})
	var list []ast.Stmt
	calls := map[string]int{}
	for _, s := range main.Body.List {
		name, call := phaseCall(s)
		if name == "" {
			list = append(list, s)
			continue
		}
		for i, want := range []string{"r", "seq"} {
			if id, ok := call.Args[i].(*ast.Ident); !ok || id.Name != want {
				t.Fatalf("%s: main() calls %s without its own %s", fset.Position(call.Pos()), name, want)
			}
		}
		calls[name]++
		list = append(list, phases[name].Body.List...)
	}
	for name := range stepPhases {
		if calls[name] != 1 || nested[name] != 1 {
			t.Fatalf("main() calls %s %d times as a statement and %d times in all; want once, as a statement",
				name, calls[name], nested[name])
		}
	}
	return &ast.BlockStmt{Lbrace: main.Body.Lbrace, List: list, Rbrace: main.Body.Rbrace}, fset
}

// beginArg returns the argument of a seq.begin(...) call, or nil.
func beginArg(n ast.Node) ast.Expr {
	call, ok := n.(*ast.CallExpr)
	if !ok {
		return nil
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "begin" {
		return nil
	}
	if x, ok := sel.X.(*ast.Ident); !ok || x.Name != "seq" || len(call.Args) != 1 {
		return nil
	}
	return call.Args[0]
}

func containsBegin(n ast.Node) bool {
	found := false
	ast.Inspect(n, func(n ast.Node) bool {
		if beginArg(n) != nil {
			found = true
		}
		return !found
	})
	return found
}

// mainBeginCalls lists the step names main() passes to seq.begin, in
// source order.
func mainBeginCalls(t *testing.T) []string {
	t.Helper()
	consts := stepConsts(t)
	body, _ := mainBody(t)
	var names []string
	ast.Inspect(body, func(n ast.Node) bool {
		arg := beginArg(n)
		if arg == nil {
			return true
		}
		id, ok := arg.(*ast.Ident)
		if !ok || consts[id.Name] == "" {
			t.Errorf("seq.begin is called with %T, not a step constant from steps.go", arg)
			return true
		}
		names = append(names, consts[id.Name])
		return true
	})
	return names
}

// replay walks main()'s body for one recipe and returns the steps main()
// announces for it.
type replay struct {
	t      *testing.T
	fset   *token.FileSet
	r      *recipe.Recipe
	consts map[string]string
	out    []string
}

func (p *replay) src(n ast.Node) string {
	var b bytes.Buffer
	_ = printer.Fprint(&b, p.fset, n)
	return b.String()
}

func (p *replay) stmts(list []ast.Stmt) {
	for _, s := range list {
		p.stmt(s)
	}
}

func (p *replay) stmt(s ast.Stmt) {
	if !containsBegin(s) {
		return
	}
	switch s := s.(type) {
	case *ast.ExprStmt:
		if arg := beginArg(s.X); arg != nil {
			id, ok := arg.(*ast.Ident)
			if !ok || p.consts[id.Name] == "" {
				p.t.Fatalf("%s: seq.begin needs a step constant", p.fset.Position(s.Pos()))
			}
			p.out = append(p.out, p.consts[id.Name])
			return
		}
	case *ast.BlockStmt:
		p.stmts(s.List)
		return
	case *ast.IfStmt:
		if s.Init != nil {
			p.t.Fatalf("%s: an if with an init statement holds a step; not modelled", p.fset.Position(s.Pos()))
		}
		if p.cond(s.Cond) {
			p.stmts(s.Body.List)
		} else if s.Else != nil {
			p.stmt(s.Else)
		}
		return
	}
	p.t.Fatalf("%s: a step inside %T is not modelled by this test:\n%s", p.fset.Position(s.Pos()), s, p.src(s))
}

// cond evaluates a condition that guards a step. Only the recipe's own
// methods (r.M() with no arguments, returning bool), !, && and || are
// allowed: those are what make the plan (shapeOf) and main() agree.
func (p *replay) cond(e ast.Expr) bool {
	switch e := e.(type) {
	case *ast.ParenExpr:
		return p.cond(e.X)
	case *ast.UnaryExpr:
		if e.Op == token.NOT {
			return !p.cond(e.X)
		}
	case *ast.BinaryExpr:
		if e.Op == token.LAND {
			return p.cond(e.X) && p.cond(e.Y)
		}
		if e.Op == token.LOR {
			return p.cond(e.X) || p.cond(e.Y)
		}
	case *ast.CallExpr:
		sel, ok := e.Fun.(*ast.SelectorExpr)
		if x, isIdent := sel.X.(*ast.Ident); ok && isIdent && x.Name == "r" && len(e.Args) == 0 {
			m := reflect.ValueOf(p.r).MethodByName(sel.Sel.Name)
			if m.IsValid() && m.Type().NumIn() == 0 && m.Type().NumOut() == 1 && m.Type().Out(0).Kind() == reflect.Bool {
				return m.Call(nil)[0].Bool()
			}
		}
	}
	p.t.Fatalf("%s: the condition `%s` guards a step; only recipe methods (r.IsManualLayout(), ...), !, && and || are modelled",
		p.fset.Position(e.Pos()), p.src(e))
	return false
}

// stepVariants are the recipe shapes of the existing test recipes and the
// VM matrix: plain, LUKS, TPM2, manual layout, ZFS, btrfs subvolumes,
// source reuse, a /var disk, systemd-boot. Each is a recipe Problems
// accepts, so no variant is a state the install never sees.
func stepVariants(t *testing.T) map[string]*recipe.Recipe {
	t.Helper()
	dir := t.TempDir()
	dev := func(name string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	sda, sdb := dev("sda"), dev("sdb")
	sda1, sda2, sda3 := dev("sda1"), dev("sda2"), dev("sda3")
	base := func(mut func(r *recipe.Recipe)) *recipe.Recipe {
		r := &recipe.Recipe{Disk: sda, Filesystem: "xfs", Image: "ghcr.io/example/os:stable",
			Hostname: "fixture", Encryption: recipe.Encryption{Type: "none"}}
		mut(r)
		return r
	}
	manual := []recipe.CustomMount{
		{Partition: sda3, Target: "/", Fstype: "xfs"},
		{Partition: sda2, Target: "/boot", Fstype: "ext4"},
		{Partition: sda1, Target: "/boot/efi", Fstype: "unformatted"},
	}
	return map[string]*recipe.Recipe{
		"plain": base(func(r *recipe.Recipe) {}),
		"luks": base(func(r *recipe.Recipe) {
			r.Encryption = recipe.Encryption{Type: "luks-passphrase", Passphrase: "correct horse"}
		}),
		"tpm2": base(func(r *recipe.Recipe) { r.Encryption = recipe.Encryption{Type: "tpm2-luks"} }),
		"tpm2+passphrase": base(func(r *recipe.Recipe) {
			r.Encryption = recipe.Encryption{Type: "tpm2-luks-passphrase", Passphrase: "correct horse"}
		}),
		"manual": base(func(r *recipe.Recipe) { r.Disk, r.Filesystem, r.CustomMounts = "", "", manual }),
		"manual+varDisk": base(func(r *recipe.Recipe) {
			r.Disk, r.Filesystem, r.CustomMounts = "", "", manual
			r.VarDisk = &recipe.VarDiskSpec{Disk: sdb}
		}),
		"zfs": base(func(r *recipe.Recipe) {
			r.Filesystem = "zfs"
			r.Experimental = []string{recipe.ExperimentalZFS}
		}),
		"btrfs subvolumes": base(func(r *recipe.Recipe) { r.Filesystem, r.BtrfsSubvolumes = "btrfs", true }),
		"systemd-boot composefs": base(func(r *recipe.Recipe) {
			r.Filesystem, r.Bootloader, r.ComposeFsBackend = "btrfs", "systemd", true
		}),
		"systemd-boot luks": base(func(r *recipe.Recipe) {
			r.Filesystem, r.Bootloader, r.ComposeFsBackend = "btrfs", "systemd", true
			r.Encryption = recipe.Encryption{Type: "luks-passphrase", Passphrase: "correct horse"}
		}),
		"source reuse": base(func(r *recipe.Recipe) {
			r.Image, r.TargetImgref = "", "ghcr.io/example/os:stable"
			r.SourceReuse = &recipe.SourceReuse{Store: "bootc", Platform: "x86_64",
				Digest: "sha256:1111111111111111111111111111111111111111111111111111111111111111"}
			r.Experimental = []string{recipe.ExperimentalBootcStoreReuse}
		}),
		"varDisk format": base(func(r *recipe.Recipe) { r.VarDisk = &recipe.VarDiskSpec{Disk: sdb} }),
		"varDisk keep": base(func(r *recipe.Recipe) {
			r.VarDisk = &recipe.VarDiskSpec{Disk: sdb, KeepExisting: true}
		}),
	}
}

// TestMainAnnouncesThePlan is the step contract: for every variant, and
// with and without a pull, main() announces exactly the planned steps, in
// order, so the total it announced up front is the number of steps the
// frontend sees.
func TestMainAnnouncesThePlan(t *testing.T) {
	body, fset := mainBody(t)
	consts := stepConsts(t)
	for name, r := range stepVariants(t) {
		if ps := r.Problems(); len(ps) > 0 {
			t.Fatalf("variant %q is not a recipe the install accepts: %v", name, ps)
		}
		for _, pull := range []bool{true, false} {
			plan := planSteps(r, pull)
			p := &replay{t: t, fset: fset, r: r, consts: consts}
			p.stmts(body.List)

			var want []string
			for _, s := range plan {
				want = append(want, s.name)
			}
			if !reflect.DeepEqual(p.out, want) {
				t.Errorf("%s (pull=%v): main() announces %v\nthe plan is %v", name, pull, p.out, want)
			}

			// Feed what main() announces through the real sequencer.
			seq := newStepSequencer(plan)
			var got []string
			last := -1
			seq.emitFn = func(step, total int, n string, cum, weight int) {
				if total != len(plan) {
					t.Errorf("%s: step %q announced with total %d, the plan has %d", name, n, total, len(plan))
				}
				if step != len(got)+1 {
					t.Errorf("%s: step %q is number %d, want %d", name, n, step, len(got)+1)
				}
				if cum < last {
					t.Errorf("%s: step %q starts at %d%%, before the previous step", name, n, cum)
				}
				last = cum
				got = append(got, n)
				if step == total && cum+weight != 100 {
					t.Errorf("%s: last step ends at %d%%, want 100", name, cum+weight)
				}
			}
			for _, n := range p.out {
				seq.begin(n)
			}
			if err := seq.check(); err != nil {
				t.Errorf("%s (pull=%v): %v", name, pull, err)
			}
			if len(got) != seq.total() {
				t.Errorf("%s (pull=%v): %d steps announced, total %d", name, pull, len(got), seq.total())
			}
		}
	}
}

// The variants reach every step at least once, and every shape the
// variants have is one the shape tests below pin.
func TestStepVariantsCoverEveryStep(t *testing.T) {
	seen := map[string]bool{}
	for _, r := range stepVariants(t) {
		for _, s := range planSteps(r, true) {
			seen[s.name] = true
		}
	}
	for c, n := range stepConsts(t) {
		if !seen[n] {
			t.Errorf("no variant reaches step %s (%q)", c, n)
		}
	}
}

// Only main() and the step phases it calls (stepPhases) announce steps.
func TestOnlyMainAnnouncesSteps(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || (file == "main.go" && fn.Name.Name == "main" && fn.Recv == nil) ||
				(fn.Recv == nil && stepPhases[fn.Name.Name] == file) {
				continue
			}
			if containsBegin(fn) {
				t.Errorf("%s: %s announces a step; only main() and the phases in stepPhases may, so TestMainAnnouncesThePlan sees it", file, fn.Name.Name)
			}
			ast.Inspect(fn, func(n ast.Node) bool {
				if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == "Step" {
					if x, ok := sel.X.(*ast.Ident); ok && x.Name == "progress" && fn.Name.Name != "newStepSequencer" {
						t.Errorf("%s: %s calls progress.Step; announce steps through the sequencer", fset.Position(n.Pos()), fn.Name.Name)
					}
				}
				return true
			})
		}
	}
}

// The sequencer reports a step out of plan and a planned step never
// announced, and announces both anyway.
func TestStepSequencerReportsDrift(t *testing.T) {
	plan := planShape(stepShape{}, true)
	type ev struct {
		step, total int
		name        string
	}
	record := func(s *stepSequencer) *[]ev {
		var evs []ev
		s.emitFn = func(step, total int, n string, _, _ int) { evs = append(evs, ev{step, total, n}) }
		return &evs
	}

	ok := newStepSequencer(plan)
	evs := record(ok)
	for _, s := range plan {
		ok.begin(s.name)
	}
	if err := ok.check(); err != nil {
		t.Errorf("the plan in order: %v", err)
	}
	if len(*evs) != len(plan) || (*evs)[len(plan)-1].step != len(plan) {
		t.Errorf("events %v, want %d numbered 1..%d", *evs, len(plan), len(plan))
	}

	short := newStepSequencer(plan)
	record(short)
	short.begin(plan[0].name)
	if err := short.check(); err == nil || !strings.Contains(err.Error(), "never announced") {
		t.Errorf("a short run: check() = %v, want the missing steps", err)
	}

	wrong := newStepSequencer(plan)
	record(wrong)
	wrong.begin(stepEncryption)
	if err := wrong.check(); err == nil || !strings.Contains(err.Error(), `the plan says "Partitioning disk"`) {
		t.Errorf("a step out of order: check() = %v", err)
	}

	extra := newStepSequencer(plan)
	evs = record(extra)
	for _, s := range plan {
		extra.begin(s.name)
	}
	extra.begin(stepEnrollTPM2)
	if err := extra.check(); err == nil || !strings.Contains(err.Error(), "past the end") {
		t.Errorf("a step past the plan: check() = %v", err)
	}
	if last := (*evs)[len(*evs)-1]; last.step != len(plan) || last.total != len(plan) {
		t.Errorf("a step past the plan is announced as %+v, want step = total = %d", last, len(plan))
	}
}
