package probe

import (
	"errors"
	"strings"
	"testing"
)

// SourceExecutionRoot is what the install asks before the disk is touched
// (#363). It agrees with Capabilities' execution_root and runs no feature
// command: only `bootc status --json` and the store reads.
func TestSourceExecutionRoot(t *testing.T) {
	const name = "ghcr.io/example/os:stable"
	src := Selection{Source: "containers-storage:" + name, TargetImgref: name}
	ostreeHost := statusJSON(name, digestHost, BackendOstree)
	matching := map[string]string{storePath: storeJSON(name, digestHost, digestList)}
	for _, c := range []struct {
		name        string
		files       map[string]string
		status      string
		statusErr   error
		sel         Selection
		established bool
		reason      string
	}{
		{"established: the host booted the source", matching, ostreeHost, nil, src, true, RootDigestMatch},
		{"established by the list digest", map[string]string{storePath: storeJSON(name, digestOther, digestHost)}, ostreeHost, nil, src, true, RootDigestMatch},
		{"mismatch: an unrelated host", map[string]string{storePath: storeJSON(name, digestOther)}, ostreeHost, nil, src, false, RootDigestMismatch},
		{"absent: no bootc on the host", matching, "", errors.New("exec: \"bootc\": executable file not found in $PATH"), src, false, RootHostNotBootc},
		{"unknown: the host booted no digest", matching, statusJSON(name, "", BackendOstree), nil, src, false, RootHostDigestUnknown},
		{"unknown: the source is not in a readable store", nil, ostreeHost, nil, src, false, RootSourceDigestUnknown},
		{"a qualified store reference is not matched by name", matching, ostreeHost, nil,
			Selection{Source: "containers-storage:[overlay@/x+/y]" + name}, false, RootSourceDigestUnknown},
	} {
		t.Run(c.name, func(t *testing.T) {
			host := &fakeHost{canned: map[string]string{}, fail: map[string]error{}}
			if c.statusErr != nil {
				host.fail["bootc status --json"] = c.statusErr
			} else {
				host.canned["bootc status --json"] = c.status
			}
			p := newProber(t, c.files, host, nil)
			p.Selection = c.sel
			r := p.SourceExecutionRoot(nil)
			if r.Established != c.established || r.Reason != c.reason {
				t.Fatalf("got %+v, want established=%v reason=%s", r, c.established, c.reason)
			}
			if r.Established && (r.Context != ExecRootLiveHost || r.Backend != BackendOstree) {
				t.Errorf("established root %+v", r)
			}
			for _, call := range host.calls {
				if strings.HasPrefix(call, "bootc ") && call != "bootc status --json" {
					t.Errorf("ran %q", call)
				}
			}
			// The same answer as the capability report.
			q := newProber(t, c.files, &fakeHost{canned: sourceRoot(c.status, printConfigXFS, inspectCurrent), fail: host.fail}, nil)
			q.Selection = c.sel
			if caps := q.Capabilities(q.Live(), q.Offline(), q.Bootc()); caps.ExecutionRoot.Established != r.Established ||
				caps.ExecutionRoot.Reason != r.Reason {
				t.Errorf("capabilities execution_root %+v, SourceExecutionRoot %+v", caps.ExecutionRoot, r)
			}
		})
	}
}

// A store the recipe names is searched with the discovered ones.
func TestSourceExecutionRootRecipeStore(t *testing.T) {
	const name = "ghcr.io/example/os:stable"
	host := &fakeHost{canned: map[string]string{"bootc status --json": statusJSON(name, digestHost, BackendOstree)}}
	p := newProber(t, map[string]string{"srv/store/overlay-images/images.json": storeJSON(name, digestHost)}, host, nil)
	p.Selection = Selection{Source: "containers-storage:" + name}
	if r := p.SourceExecutionRoot(nil); r.Established {
		t.Fatalf("established without the recipe's store: %+v", r)
	}
	if r := p.SourceExecutionRoot([]string{"/srv/store"}); !r.Established || r.Reason != RootDigestMatch {
		t.Fatalf("with the recipe's store: %+v", r)
	}
}
