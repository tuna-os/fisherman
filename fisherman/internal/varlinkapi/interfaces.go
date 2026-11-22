package varlinkapi

import (
	"embed"

	"github.com/tuna-os/fisherman/internal/probe"
)

// The interface definitions are the contract. They are embedded so that
// org.varlink.service.GetInterfaceDescription serves exactly these files.
//
//go:embed idl/*.varlink
var idlFS embed.FS

// Interface names.
const (
	IfaceSysInstall  = "io.systemd.SysInstall"
	IfaceCryptEnroll = "io.systemd.CryptEnroll"
	IfaceShutdown    = "io.systemd.Shutdown"
	IfaceFisherman   = "io.tunaos.Fisherman"
)

var interfaceIDL = func() map[string]string {
	out := map[string]string{}
	for _, n := range []string{IfaceSysInstall, IfaceCryptEnroll, IfaceShutdown, IfaceFisherman} {
		b, err := idlFS.ReadFile("idl/" + n + ".varlink")
		if err != nil {
			panic(err) // a build error: the file is embedded
		}
		out[n] = string(b)
	}
	return out
}()

// InterfaceIDL returns the interface definition text.
func InterfaceIDL(name string) string { return interfaceIDL[name] }

func sizeLabel(n int64) string { return probe.SizeLabel(n) }
