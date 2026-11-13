package main

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/tuna-os/fisherman/internal/recipe"

	"github.com/tuna-os/wootc/slurp"
)

// The pre-partition phase (#313), run against recorded host operations:
// no audio configuration is applied and no NTFS partition is read.

type prepartitionRecorder struct {
	phaseRecorder
	ntfs       []string
	ntfsErr    error
	wallpapers map[string]*slurp.WallpaperResult
}

func (p *prepartitionRecorder) ops() prepartitionOps {
	return prepartitionOps{
		applyAudioLive: func() error { return p.rec("applyAudioLive") },
		extractData: func(cfg *slurp.SlurpConfig) (*slurp.DataSlurpResult, error) {
			var users []string
			for _, u := range cfg.Users {
				users = append(users, u.Name+":"+strings.Join(u.Categories, "+"))
			}
			if err := p.rec("extractData %s %v", cfg.SourcePartition, users); err != nil {
				return nil, err
			}
			return &slurp.DataSlurpResult{Found: true}, nil
		},
		detectNTFS: func(d string) ([]string, error) {
			p.log = append(p.log, "detectNTFS "+d)
			return p.ntfs, p.ntfsErr
		},
		extractWallpapers: func(part string) (*slurp.WallpaperResult, error) {
			if err := p.rec("extractWallpapers %s", part); err != nil {
				return nil, err
			}
			if w, ok := p.wallpapers[part]; ok {
				return w, nil
			}
			return nil, errors.New("no wallpaper directory")
		},
		info: func(msg string) { p.log = append(p.log, "info "+msg) },
	}
}

func TestPrepartitionPhase(t *testing.T) {
	found := &slurp.WallpaperResult{Found: true}
	tests := []struct {
		name    string
		mut     func(r *recipe.Recipe, p *prepartitionRecorder)
		variant string
		extras  recipe.Extras
		live    bool
		want    []string
		data    bool
		wall    bool
	}{
		{name: "nothing", variant: "plain"},
		{name: "audio, not live", variant: "plain", extras: recipe.Extras{AudioNames: true},
			want: []string{"info Live audio config: skipped, this host is not live media and its session is not the installer's to change"}},
		{name: "audio, live", variant: "plain", extras: recipe.Extras{AudioNames: true}, live: true,
			want: []string{"applyAudioLive"}},
		{name: "audio fails", variant: "plain", extras: recipe.Extras{AudioNames: true}, live: true,
			mut:  func(_ *recipe.Recipe, p *prepartitionRecorder) { p.fail = "applyAudioLive" },
			want: []string{"applyAudioLive", "info Live audio config skipped: injected failure"}},
		{name: "data", variant: "plain", data: true,
			mut: func(r *recipe.Recipe, _ *prepartitionRecorder) {
				r.Slurp = &recipe.SlurpSpec{SourcePartition: "/dev/sda4",
					Users: []recipe.SlurpUserSpec{{Name: "ada", Categories: []string{"documents", "pictures"}}}}
				r.SlurpWallpapers = true // the data slurp wins
			},
			want: []string{"info Migrating Windows user data before partitioning...", "extractData /dev/sda4 [ada:documents+pictures]"}},
		{name: "data fails", variant: "plain",
			mut: func(r *recipe.Recipe, p *prepartitionRecorder) {
				r.Slurp = &recipe.SlurpSpec{SourcePartition: "/dev/sda4"}
				p.fail = "extractData"
			},
			want: []string{"info Migrating Windows user data before partitioning...", "extractData /dev/sda4 []",
				"info Data migration skipped: injected failure"}},
		{name: "wallpapers", variant: "plain", wall: true,
			mut: func(r *recipe.Recipe, p *prepartitionRecorder) {
				r.SlurpWallpapers = true
				p.ntfs = []string{"/dev/sda4", "/dev/sda5", "/dev/sda6"}
				p.wallpapers = map[string]*slurp.WallpaperResult{"/dev/sda5": found, "/dev/sda6": found}
			},
			want: []string{"info Checking for Windows wallpapers to migrate...", "detectNTFS /dev/sda",
				"info Found 3 NTFS partition(s), extracting wallpapers",
				"extractWallpapers /dev/sda4", "info Wallpaper extraction from /dev/sda4 skipped: no wallpaper directory",
				"extractWallpapers /dev/sda5"}},
		{name: "no wallpapers", variant: "plain",
			mut: func(r *recipe.Recipe, p *prepartitionRecorder) {
				r.SlurpWallpapers = true
				p.ntfsErr = errors.New("blkid failed")
			},
			want: []string{"info Checking for Windows wallpapers to migrate...", "detectNTFS /dev/sda",
				"info NTFS detection skipped: blkid failed", "info No Windows wallpapers found — continuing normally"}},
		{name: "manual layout", variant: "manual",
			mut: func(r *recipe.Recipe, _ *prepartitionRecorder) {
				r.Slurp = &recipe.SlurpSpec{SourcePartition: "/dev/sda4"}
				r.SlurpWallpapers = true
			}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := phaseVariant(t, tt.variant)
			p := &prepartitionRecorder{}
			if tt.mut != nil {
				tt.mut(r, p)
			}
			res := prepartitionPhase(r, tt.extras, func() bool { return tt.live }, p.ops())
			if !reflect.DeepEqual(p.log, tt.want) {
				t.Errorf("log:\n  %s\nwant:\n  %s", strings.Join(p.log, "\n  "), strings.Join(tt.want, "\n  "))
			}
			if (res.dataResult != nil) != tt.data || (res.wallpaperResult != nil) != tt.wall {
				t.Errorf("result = %+v", res)
			}
			if tt.wall && res.wallpaperResult != found {
				t.Errorf("wallpapers from the wrong partition")
			}
		})
	}
}

func TestLivePrepartitionOpsSetsEveryOperation(t *testing.T) {
	v := reflect.ValueOf(livePrepartitionOps())
	for i := 0; i < v.NumField(); i++ {
		if v.Field(i).IsNil() {
			t.Errorf("livePrepartitionOps leaves %s unset", v.Type().Field(i).Name)
		}
	}
}
