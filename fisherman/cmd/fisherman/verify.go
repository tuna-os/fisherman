package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tuna-os/fisherman/internal/receipt"
	"github.com/tuna-os/fisherman/internal/runner"
)

const verifyUsage = `usage: fisherman verify --json [--root <dir>] [--live | --collected]
                        [--receipt <file>] [--collection <dir>] [--write]

Compare the installation receipt fisherman wrote at install time with what
the installed system booted, and print one JSON result (docs/RECEIPT.md).
Exit 0 only when the outcome is "verified".

  --root <dir>        the installed system's root (default /)
  --live              run bootc status now; only with --root /
  --collected         read what the first-boot unit collected
                      (default when --root is not / or --collection is given)
  --receipt <file>    the receipt (default <root>` + receipt.ReceiptPath + `)
  --collection <dir>  the collection (default <root>` + receipt.CollectionDir + `)
  --write             also store the result as <collection>/` + receipt.ResultFile + `

It reads; it never switches an image, changes the boot order, rolls back or
reboots. The host's bootc never answers for another root.
`

// verifyRun runs the read-only bootc commands of `fisherman verify`.
var verifyRun = func(name string, args ...string) ([]byte, error) {
	return runner.BoundedOutput(probeCommandTimeout, probeMaxOutput, name, args...)
}

// verifyNow is the clock; tests fix it.
var verifyNow = time.Now

// maxReceiptFile caps every file verify reads.
const maxReceiptFile = 8 << 20

func readBounded(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxReceiptFile+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxReceiptFile {
		return nil, fmt.Errorf("%s: larger than %d bytes", path, maxReceiptFile)
	}
	return b, nil
}

// runVerify implements `fisherman verify --json` and returns the exit code.
func runVerify(args []string, stdout, stderr io.Writer) int {
	var (
		jsonOut, live, collected, write bool
		root                            = "/"
		receiptPath, collectionDir      string
	)
	for i := 0; i < len(args); i++ {
		a := args[i]
		flag, value, hasValue := strings.Cut(a, "=")
		switch flag {
		case "--root", "--receipt", "--collection":
			if !hasValue {
				if i+1 >= len(args) {
					fmt.Fprintf(stderr, "verify: %s needs a value\n\n%s", flag, verifyUsage)
					return exitUsage
				}
				i++
				value = args[i]
			}
			if value == "" {
				fmt.Fprintf(stderr, "verify: %s needs a value\n\n%s", flag, verifyUsage)
				return exitUsage
			}
			switch flag {
			case "--root":
				root = value
			case "--receipt":
				receiptPath = value
			default:
				collectionDir = value
			}
			continue
		}
		switch a {
		case "--json":
			jsonOut = true
		case "--live":
			live = true
		case "--collected":
			collected = true
		case "--write":
			write = true
		case "-h", "--help", "help":
			fmt.Fprint(stdout, verifyUsage)
			return 0
		default:
			fmt.Fprintf(stderr, "verify: unknown argument %q\n\n%s", a, verifyUsage)
			return exitUsage
		}
	}
	if !jsonOut {
		fmt.Fprintf(stderr, "verify: --json is required (it is the only output format)\n\n%s", verifyUsage)
		return exitUsage
	}
	if live && collected {
		fmt.Fprintf(stderr, "verify: --live and --collected exclude each other\n\n%s", verifyUsage)
		return exitUsage
	}
	root = filepath.Clean(root)
	if live && (root != "/" || collectionDir != "") {
		// bootc status describes the system it runs on: the root is /.
		fmt.Fprintf(stderr, "verify: --live reads the running system; it needs --root / and no --collection\n\n%s", verifyUsage)
		return exitUsage
	}
	if !live && !collected {
		collected = root != "/" || collectionDir != ""
	}
	if receiptPath == "" {
		receiptPath = filepath.Join(root, receipt.ReceiptPath)
	}
	if collectionDir == "" {
		collectionDir = filepath.Join(root, receipt.CollectionDir)
	}

	in := receipt.Input{FishermanVersion: version, Now: verifyNow()}
	in.Receipt, in.ReceiptErr = readBounded(receiptPath)
	if collected {
		in.Evidence = receipt.EvidenceCollected
		in.Collection, in.CollectionErr = readBounded(filepath.Join(collectionDir, receipt.CollectionFile))
		in.Status, in.StatusErr = readBounded(filepath.Join(collectionDir, receipt.StatusFile))
		if v, err := readBounded(filepath.Join(collectionDir, receipt.VersionFile)); err == nil {
			in.BootcVersion, _, _ = strings.Cut(string(v), "\n")
		}
	} else {
		in.Evidence = receipt.EvidenceLive
		// Only after a receipt: without one there is nothing to compare,
		// and bootc need not run at all.
		if in.ReceiptErr == nil {
			in.Status, in.StatusErr = verifyRun("bootc", "status", "--format", "json", "--format-version", "1")
			if in.StatusErr == nil {
				if v, err := verifyRun("bootc", "--version"); err == nil {
					in.BootcVersion, _, _ = strings.Cut(string(v), "\n")
				}
			}
		}
	}
	res := receipt.Evaluate(in)

	out, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		fmt.Fprintf(stderr, "verify: encoding output: %v\n", err)
		return exitFailure
	}
	out = append(out, '\n')
	if write {
		if err := writeResult(collectionDir, out); err != nil {
			fmt.Fprintf(stderr, "verify: --write: %v\n", err)
			_, _ = stdout.Write(out)
			return exitFailure
		}
	}
	if _, err := stdout.Write(out); err != nil {
		return exitFailure
	}
	if res.Outcome != receipt.OutcomeVerified {
		return exitFailure
	}
	return 0
}

// writeResult stores the result atomically in dir.
func writeResult(dir string, data []byte) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+receipt.ResultFile+".*")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		_ = os.Remove(tmp.Name())
		if werr != nil {
			return werr
		}
		return cerr
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), filepath.Join(dir, receipt.ResultFile)); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return nil
}
