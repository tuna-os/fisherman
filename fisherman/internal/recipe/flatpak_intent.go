package recipe

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
)

// FlatpakIntent is an opt-in contract. Absence preserves legacy Flatpaks behavior.
// Inherit preserves the image policy; replace with apps:[] explicitly selects none.
type FlatpakIntent struct {
	Version int              `json:"version"`
	Live    FlatpakSelection `json:"live"`
	Target  FlatpakSelection `json:"target"`
}
type FlatpakSelection struct {
	Mode string       `json:"mode"`
	Apps []FlatpakApp `json:"apps"`
}
type FlatpakApp struct {
	ID     string        `json:"id"`
	Branch string        `json:"branch"`
	Arch   string        `json:"arch"`
	Source FlatpakSource `json:"source"`
}
type FlatpakSource struct {
	Kind     string `json:"kind"`
	Identity string `json:"identity"`
}

const FlathubIdentity = "https://dl.flathub.org/repo/"

var appIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+(\.[A-Za-z0-9_-]+){2,}$`)
var branchPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// Strictness is scoped to the new envelope. Legacy Recipe fields retain their
// existing decoding behavior, while misspelled intent fields fail closed.
func (i *FlatpakIntent) UnmarshalJSON(data []byte) error {
	type wire FlatpakIntent
	var value wire
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&value); err != nil {
		return err
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("trailing Flatpak intent data")
	}
	*i = FlatpakIntent(value)
	return nil
}

func (i *FlatpakIntent) Validate() error {
	if i == nil {
		return nil
	}
	if i.Version != 1 {
		return fmt.Errorf("unsupported flatpakIntent version %d", i.Version)
	}
	if i.Live.Mode != "inherit" {
		return fmt.Errorf("live replace is not supported by this target-only consumer")
	}
	for _, entry := range []struct {
		scope     string
		selection FlatpakSelection
	}{{"live", i.Live}, {"target", i.Target}} {
		scope, s := entry.scope, entry.selection
		if s.Mode != "inherit" && s.Mode != "replace" {
			return fmt.Errorf("flatpakIntent %s: mode must be inherit or replace", scope)
		}
		if s.Mode == "replace" && s.Apps == nil {
			return fmt.Errorf("flatpakIntent %s: replace requires explicit apps array", scope)
		}
		if s.Mode == "inherit" && len(s.Apps) > 0 {
			return fmt.Errorf("flatpakIntent %s: inherit cannot carry apps", scope)
		}
		for _, a := range s.Apps {
			if !appIDPattern.MatchString(a.ID) || !branchPattern.MatchString(a.Branch) || (a.Arch != "x86_64" && a.Arch != "aarch64") {
				return fmt.Errorf("flatpakIntent %s: invalid full app identity", scope)
			}
			if (a.Source.Kind != "remote" && a.Source.Kind != "bundle") || a.Source.Identity == "" {
				return fmt.Errorf("flatpakIntent %s: missing source identity", scope)
			}
			// Do not reinterpret bundle or third-party remote IDs as Flathub IDs.
			// They remain representable for transport, but this consumer fails closed.
			if scope == "target" && (a.Source.Kind != "remote" || a.Source.Identity != FlathubIdentity) {
				return fmt.Errorf("flatpakIntent target: source %q is not supported by this consumer", a.Source.Identity)
			}
		}
	}
	return nil
}
