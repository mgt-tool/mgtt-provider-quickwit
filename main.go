// Command mgtt-provider-quickwit is a quickwit provider runner for mgtt.
// All plumbing (argv parsing, JSON output, exit codes, status:not_found
// translation) lives in github.com/mgt-tool/mgtt/sdk/provider — this file
// only wires probes.
package main

import (
	"github.com/mgt-tool/mgtt-provider-quickwit/internal/probes"
	"github.com/mgt-tool/mgtt/sdk/provider"
)

func main() {
	r := provider.NewRegistry()
	probes.Register(r)
	provider.Main(r)
}
