package main

import (
	"reflect"
	"strings"
	"testing"

	"github.com/tuna-os/fisherman/internal/recipe"
)

// The pre-partition phase (#313), run against recorded host operations:
// no audio configuration is applied.

type prepartitionRecorder struct {
	phaseRecorder
}

func (p *prepartitionRecorder) ops() prepartitionOps {
	return prepartitionOps{
		applyAudioLive: func() error { return p.rec("applyAudioLive") },
		info:           func(msg string) { p.log = append(p.log, "info "+msg) },
	}
}

func TestPrepartitionPhase(t *testing.T) {
	tests := []struct {
		name   string
		fail   string
		extras recipe.Extras
		live   bool
		want   []string
	}{
		{name: "nothing"},
		{name: "nothing, live", live: true},
		{name: "audio, not live", extras: recipe.Extras{AudioNames: true},
			want: []string{"info Live audio config: skipped, this host is not live media and its session is not the installer's to change"}},
		{name: "audio, live", extras: recipe.Extras{AudioNames: true}, live: true,
			want: []string{"applyAudioLive"}},
		{name: "audio fails", extras: recipe.Extras{AudioNames: true}, live: true, fail: "applyAudioLive",
			want: []string{"applyAudioLive", "info Live audio config skipped: injected failure"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &prepartitionRecorder{}
			p.fail = tt.fail
			prepartitionPhase(tt.extras, func() bool { return tt.live }, p.ops())
			if !reflect.DeepEqual(p.log, tt.want) {
				t.Errorf("log:\n  %s\nwant:\n  %s", strings.Join(p.log, "\n  "), strings.Join(tt.want, "\n  "))
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
