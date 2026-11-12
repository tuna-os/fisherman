package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/tuna-os/fisherman/internal/diagnostics"
	"github.com/tuna-os/fisherman/internal/probe"
	"github.com/tuna-os/fisherman/internal/recipe"
)

const diagnosticsUsage = `usage: fisherman diagnostics (--output-dir <dir> | --stdout)
                            [--recipe <file|->] [--progress-log <file|->]
                            [--probe | --probe-report <file|->]
                            [--include-identifiers]

Write a local support bundle: one JSON file of allowlisted facts about an
install (versions, probe facts, the recipe's effective options, the progress
timeline and outcome, what is still mounted). It never holds a passphrase,
recovery key, password, password hash or token; host, user and device
identifiers are masked unless --include-identifiers. The file is created
mode 0600 under a new unique name. Review it before you share it: fisherman
never uploads it. Contract: docs/DIAGNOSTICS.md.

  --output-dir <dir>      write the bundle to a new file in <dir>
  --stdout                print the bundle instead, for review
  --recipe <file|->       the recipe of the install (never copied whole)
  --progress-log <file|-> the install's saved stdout (NDJSON progress)
  --probe                 run the read-only probe now (bounded in time)
  --probe-report <file|-> a saved 'fisherman probe --json' report
  --include-identifiers   keep host names, user names and device paths

At most one of --recipe and --progress-log reads stdin. The export only
reads: it does not mount, unlock, retry or clean up anything.
`

// Seams for tests.
var (
	diagnosticsProbe        = func() (*probe.Result, error) { return newProber().Probe() }
	diagnosticsProbeTimeout = 60 * time.Second
	diagnosticsRoot         = "/"
	diagnosticsNow          = time.Now
)

// maxProbeReport bounds a saved probe report.
const maxProbeReport = 8 << 20

// runDiagnostics implements `fisherman diagnostics` and returns the exit
// code. It is a separate command: the install never runs it, so a failed
// export cannot change an install's outcome or exit code.
func runDiagnostics(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	var outDir, recipeArg, progressArg, probeArg string
	toStdout, liveProbe, identifiers := false, false, false
	usage := func(format string, a ...any) int {
		fmt.Fprintf(stderr, "diagnostics: "+format+"\n\n%s", append(a, diagnosticsUsage)...)
		return exitUsage
	}
	for i := 0; i < len(args); i++ {
		flag, value, hasValue := strings.Cut(args[i], "=")
		switch flag {
		case "--output-dir", "--recipe", "--progress-log", "--probe-report":
			if !hasValue {
				if i+1 >= len(args) {
					return usage("%s needs a value", flag)
				}
				i++
				value = args[i]
			}
			if value == "" {
				return usage("%s needs a value", flag)
			}
			switch flag {
			case "--output-dir":
				outDir = value
			case "--recipe":
				recipeArg = value
			case "--progress-log":
				progressArg = value
			case "--probe-report":
				probeArg = value
			}
			continue
		}
		switch args[i] {
		case "--stdout":
			toStdout = true
		case "--probe":
			liveProbe = true
		case "--include-identifiers":
			identifiers = true
		case "-h", "--help", "help":
			fmt.Fprint(stdout, diagnosticsUsage)
			return 0
		default:
			return usage("unknown argument %q", args[i])
		}
	}
	if (outDir == "") == !toStdout {
		return usage("give exactly one of --output-dir and --stdout")
	}
	if liveProbe && probeArg != "" {
		return usage("--probe and --probe-report exclude each other")
	}
	stdinUsers := 0
	for _, a := range []string{recipeArg, progressArg, probeArg} {
		if a == recipe.StdinArg {
			stdinUsers++
		}
	}
	if stdinUsers > 1 {
		return usage("only one input can be read from stdin")
	}

	in := diagnostics.Inputs{
		Versions:           diagnostics.Versions{Fisherman: version, Go: runtime.Version(), OS: runtime.GOOS, Arch: runtime.GOARCH},
		Root:               diagnosticsRoot,
		IncludeIdentifiers: identifiers,
		Now:                diagnosticsNow(),
	}
	if recipeArg != "" {
		in.Recipe, in.RecipeErr = readInput(recipeArg, stdin, recipe.MaxRecipeBytes)
	}
	if progressArg != "" {
		in.Progress, in.ProgressTruncated, in.ProgressErr = readProgressLog(progressArg, stdin)
	}
	switch {
	case probeArg != "":
		data, err := readInput(probeArg, stdin, maxProbeReport)
		var res probe.Result
		if err == nil && json.Unmarshal(data, &res) == nil {
			in.Probe = &res
		} else {
			in.ProbeStatus = diagnostics.StatusUnreadable
		}
	case liveProbe:
		in.Probe, in.ProbeStatus = runBoundedProbe()
	}

	data, err := diagnostics.Marshal(diagnostics.Build(in))
	if err != nil {
		fmt.Fprintf(stderr, "diagnostics: %v\n", err)
		return exitFailure
	}
	if toStdout {
		if _, err := stdout.Write(data); err != nil {
			fmt.Fprintf(stderr, "diagnostics: %v\n", err)
			return exitFailure
		}
		return 0
	}
	path, err := diagnostics.Write(outDir, in.Now, data)
	if err != nil {
		fmt.Fprintf(stderr, "diagnostics: %v\n", err)
		return exitFailure
	}
	fmt.Fprintln(stdout, path)
	fmt.Fprintln(stderr, "diagnostics: review the bundle before you share it; fisherman never uploads it")
	return 0
}

// runBoundedProbe runs the probe, giving up after diagnosticsProbeTimeout.
func runBoundedProbe() (*probe.Result, string) {
	type answer struct {
		res *probe.Result
		err error
	}
	ch := make(chan answer, 1)
	go func() {
		res, err := diagnosticsProbe()
		ch <- answer{res, err}
	}()
	select {
	case a := <-ch:
		if a.err != nil || a.res == nil {
			return nil, diagnostics.StatusUnreadable
		}
		return a.res, ""
	case <-time.After(diagnosticsProbeTimeout):
		return nil, diagnostics.StatusTimedOut
	}
}

// readInput reads a whole input of at most max bytes; "-" is stdin.
func readInput(arg string, stdin io.Reader, max int64) ([]byte, error) {
	rd := stdin
	if arg != recipe.StdinArg {
		f, err := os.Open(arg)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		rd = f
	}
	data, err := io.ReadAll(io.LimitReader(rd, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("larger than %d bytes", max)
	}
	return data, nil
}

// readProgressLog reads the end of a progress log, at most
// diagnostics.MaxProgressBytes; truncated says the start was left out.
func readProgressLog(arg string, stdin io.Reader) ([]byte, bool, error) {
	const max = diagnostics.MaxProgressBytes
	if arg != recipe.StdinArg {
		f, err := os.Open(arg)
		if err != nil {
			return nil, false, err
		}
		defer f.Close()
		if fi, err := f.Stat(); err == nil && fi.Mode().IsRegular() && fi.Size() > max {
			if _, err := f.Seek(fi.Size()-max, io.SeekStart); err != nil {
				return nil, false, err
			}
			data, err := io.ReadAll(io.LimitReader(f, max))
			return data, true, err
		}
		return readTail(f, max)
	}
	return readTail(stdin, max)
}

// readTail reads rd to EOF and keeps its last max bytes.
func readTail(rd io.Reader, max int) ([]byte, bool, error) {
	buf := make([]byte, 0, 64<<10)
	chunk := make([]byte, 64<<10)
	truncated := false
	for {
		n, err := rd.Read(chunk)
		buf = append(buf, chunk[:n]...)
		if len(buf) > 2*max {
			buf = append(buf[:0], buf[len(buf)-max:]...)
			truncated = true
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, false, err
		}
	}
	if len(buf) > max {
		buf = buf[len(buf)-max:]
		truncated = true
	}
	return buf, truncated, nil
}
