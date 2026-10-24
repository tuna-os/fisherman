package post_test

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tuna-os/fisherman/internal/post"
	"github.com/tuna-os/fisherman/internal/recipe"
	"github.com/tuna-os/fisherman/internal/runner"
)

type intentExecutor struct {
	calls     []string
	fail      string
	onCommand func(string)
}

func (e *intentExecutor) Command(name string, args ...string) runner.Command {
	call := name + " " + strings.Join(args, " ")
	e.calls = append(e.calls, call)
	if e.onCommand != nil {
		e.onCommand(call)
	}
	var err error
	if e.fail != "" && strings.Contains(call, e.fail) {
		err = errors.New("injected failure")
	}
	return &mockCommand{err: err}
}
func setupIntent(t *testing.T, compose bool) (string, string, *intentExecutor) {
	t.Helper()
	root := t.TempDir()
	etc := filepath.Join(root, "deployment", "etc")
	if err := os.MkdirAll(etc, 0755); err != nil {
		t.Fatal(err)
	}
	oldRun, oldExec, oldDeploy, oldCompose := runner.RunFn, post.Exec, post.DeploymentDirFn, post.ComposeFsDeployEtcDirFn
	runner.RunFn = func(_ io.Reader, name string, args ...string) error {
		if name != "ls" {
			t.Fatalf("unexpected host call: %s", name)
		}
		if strings.Contains(args[0], "state/deploy") && !compose {
			return errors.New("no composefs")
		}
		return nil
	}
	post.DeploymentDirFn = func(string) (string, error) { return filepath.Dir(etc), nil }
	post.ComposeFsDeployEtcDirFn = func(string) (string, error) { return etc, nil }
	exec := &intentExecutor{}
	post.Exec = exec
	t.Cleanup(func() {
		runner.RunFn = oldRun
		post.Exec = oldExec
		post.DeploymentDirFn = oldDeploy
		post.ComposeFsDeployEtcDirFn = oldCompose
	})
	return root, etc, exec
}
func selected(apps ...recipe.FlatpakApp) *recipe.FlatpakIntent {
	if apps == nil {
		apps = []recipe.FlatpakApp{}
	}
	return &recipe.FlatpakIntent{Version: 1, Live: recipe.FlatpakSelection{Mode: "inherit"}, Target: recipe.FlatpakSelection{Mode: "replace", Apps: apps}}
}
func firefox() recipe.FlatpakApp {
	return recipe.FlatpakApp{ID: "org.mozilla.firefox", Branch: "stable", Arch: "x86_64", Source: recipe.FlatpakSource{Kind: "remote", Identity: recipe.FlathubIdentity}}
}
func TestTargetIntentExplicitNone(t *testing.T) {
	for _, compose := range []bool{false, true} {
		t.Run(map[bool]string{true: "composefs", false: "ostree"}[compose], func(t *testing.T) {
			root, etc, e := setupIntent(t, compose)
			if err := post.ApplyTargetFlatpakIntent(root, "", selected()); err != nil {
				t.Fatal(err)
			}
			if len(e.calls) != 0 {
				t.Fatalf("empty selection invoked external commands: %v", e.calls)
			}
			if link, err := os.Readlink(filepath.Join(etc, "systemd/system/flatpak-preinstall.service")); err != nil || link != "/dev/null" {
				t.Fatalf("firstboot policy: %s %v", link, err)
			}
			data, err := os.ReadFile(filepath.Join(etc, "fisherman/flatpak-intent.json"))
			if err != nil || !strings.Contains(string(data), `"apps": []`) || !strings.Contains(string(data), `"appliedScope": "target"`) || strings.Contains(string(data), `"live"`) {
				t.Fatalf("missing explicit intent: %s %v", data, err)
			}
		})
	}
}
func TestTargetIntentDownloadsExactSelectedRef(t *testing.T) {
	root, _, e := setupIntent(t, false)
	a := firefox()
	if err := post.ApplyTargetFlatpakIntent(root, "custom/var", selected(a, a)); err != nil {
		t.Fatal(err)
	}
	calls := strings.Join(e.calls, "\n")
	if len(e.calls) != 3 || !strings.Contains(calls, "install --noninteractive -y flathub app/org.mozilla.firefox/x86_64/stable") || !strings.Contains(calls, "info app/org.mozilla.firefox/x86_64/stable") {
		t.Fatalf("wrong runtime calls: %s", calls)
	}
	if strings.Contains(calls, "tar ") || !strings.Contains(calls, "FLATPAK_SYSTEM_DIR="+root+"/custom/var/lib/.fisherman-flatpak-") {
		t.Fatalf("not isolated: %s", calls)
	}
}
func TestTargetIntentFailureIsNotApplied(t *testing.T) {
	for _, failure := range []string{"remote-add", "install --", "info app/"} {
		t.Run(failure, func(t *testing.T) {
			root, etc, e := setupIntent(t, false)
			e.fail = failure
			if err := post.ApplyTargetFlatpakIntent(root, "", selected(firefox())); err == nil {
				t.Fatal("failure hidden")
			}
			if _, err := os.Lstat(filepath.Join(etc, "systemd/system/flatpak-preinstall.service")); !os.IsNotExist(err) {
				t.Fatal("failed selection changed firstboot policy")
			}
			if _, err := os.Stat(filepath.Join(root, "ostree/deploy/default/var/lib/flatpak")); !os.IsNotExist(err) {
				t.Fatal("partial repository published")
			}
		})
	}
}
func TestTargetIntentRefusesUnsafeOrExistingPaths(t *testing.T) {
	for _, which := range []string{"traversal", "existing", "symlink"} {
		t.Run(which, func(t *testing.T) {
			root, etc, e := setupIntent(t, false)
			varPath := "var"
			switch which {
			case "traversal":
				varPath = "../outside"
			case "existing":
				os.MkdirAll(filepath.Join(root, "var/lib/flatpak"), 0755)
				os.WriteFile(filepath.Join(root, "var/lib/flatpak/preserve"), []byte("x"), 0644)
			case "symlink":
				os.Symlink(t.TempDir(), filepath.Join(etc, "systemd"))
			}
			if err := post.ApplyTargetFlatpakIntent(root, varPath, selected(firefox())); err == nil {
				t.Fatal("unsafe target accepted")
			}
			if len(e.calls) != 0 {
				t.Fatal("commands executed before guard")
			}
		})
	}
}
func TestTargetIntentAlreadyMasked(t *testing.T) {
	root, etc, _ := setupIntent(t, false)
	os.MkdirAll(filepath.Join(etc, "systemd/system"), 0755)
	os.Symlink("/dev/null", filepath.Join(etc, "systemd/system/flatpak-preinstall.service"))
	if err := post.ApplyTargetFlatpakIntent(root, "", selected()); err != nil {
		t.Fatal(err)
	}
}

func TestTargetIntentConflictingPolicyIsNotChanged(t *testing.T) {
	root, etc, e := setupIntent(t, false)
	unit := filepath.Join(etc, "systemd/system/flatpak-preinstall.service")
	os.MkdirAll(filepath.Dir(unit), 0755)
	os.WriteFile(unit, []byte("preserve policy"), 0644)
	if err := post.ApplyTargetFlatpakIntent(root, "", selected(firefox())); err == nil {
		t.Fatal("conflicting policy overwritten")
	}
	if len(e.calls) > 0 {
		t.Fatal("download started before policy guard")
	}
	data, _ := os.ReadFile(unit)
	if string(data) != "preserve policy" {
		t.Fatal("existing unit changed")
	}
}

func TestTargetIntentRefusesExistingStateBeforeWork(t *testing.T) {
	for _, kind := range []string{"regular", "outside-dangling-link", "inside-link", "dangling-parent"} {
		t.Run(kind, func(t *testing.T) {
			root, etc, e := setupIntent(t, false)
			state := filepath.Join(etc, "fisherman/flatpak-intent.json")
			outside := filepath.Join(t.TempDir(), "must-not-create")
			protected := filepath.Join(root, "protected")
			os.WriteFile(protected, []byte("preserve"), 0644)
			if kind == "dangling-parent" {
				os.Symlink(outside, filepath.Dir(state))
			} else {
				os.MkdirAll(filepath.Dir(state), 0755)
				switch kind {
				case "regular":
					os.WriteFile(state, []byte("prior result"), 0644)
				case "outside-dangling-link":
					os.Symlink(outside, state)
				case "inside-link":
					os.Symlink(protected, state)
				}
			}
			if err := post.ApplyTargetFlatpakIntent(root, "", selected(firefox())); err == nil {
				t.Fatal("existing state accepted")
			}
			if len(e.calls) > 0 {
				t.Fatal("commands ran before state guard")
			}
			if _, err := os.Lstat(outside); !os.IsNotExist(err) {
				t.Fatal("created outside target")
			}
			data, _ := os.ReadFile(protected)
			if string(data) != "preserve" {
				t.Fatal("internal file overwritten")
			}
			if kind == "regular" {
				data, _ := os.ReadFile(state)
				if string(data) != "prior result" {
					t.Fatal("prior state overwritten")
				}
			}
		})
	}
}

func TestTargetIntentPublishedRepositoryMode(t *testing.T) {
	root, _, _ := setupIntent(t, false)
	if err := post.ApplyTargetFlatpakIntent(root, "var", selected()); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(root, "var/lib/flatpak"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0755 {
		t.Fatalf("published system repo mode %o, want 0755", info.Mode().Perm())
	}
}
func TestTargetIntentPreservesFileAppearingDuringDownload(t *testing.T) {
	root, etc, e := setupIntent(t, false)
	dst := filepath.Join(root, "var/lib/flatpak")
	e.onCommand = func(call string) {
		if strings.Contains(call, " info app/") {
			if err := os.WriteFile(dst, []byte("concurrent file"), 0644); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := post.ApplyTargetFlatpakIntent(root, "var", selected(firefox())); err == nil {
		t.Fatal("concurrent file accepted")
	}
	data, err := os.ReadFile(dst)
	if err != nil || string(data) != "concurrent file" {
		t.Fatalf("file destroyed: %q %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(etc, "fisherman/flatpak-intent.json")); !os.IsNotExist(err) {
		t.Fatal("failed selection recorded as applied")
	}
}
