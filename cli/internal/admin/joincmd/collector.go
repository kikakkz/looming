// SPDX-License-Identifier: Apache-2.0

package joincmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	hostdomain "github.com/kikakkz/looming/platform/go/hostdomain"
)

// Machine-facts collection for the join payload (advisor-l1 §8 slice
// 1.3): the joining host reads its own hardware and egress reachability
// and registers them with the cluster, replacing slice 1.1's
// hand-declared capabilities. Every source degrades independently — an
// unreadable /proc or a failed probe drops that fact, never the join —
// and the operator stays the authority for what observation cannot
// know: the cloud/lan zone is a placement semantic and stays a declared
// label, never auto-assigned.

const (
	// cpuinfoPath and meminfoPath are the Linux procfs sources. Looming
	// hosts are Linux (the runtime plane is Docker); on any other OS
	// the reads fail and those facts simply stay undeclared.
	cpuinfoPath = "/proc/cpuinfo"
	meminfoPath = "/proc/meminfo"
	// rootPath is the disk-facts anchor: the advisor's disk floor
	// guards the bundle's data directory, which lives on the root
	// filesystem — so the root filesystem's available space is the
	// conservative, operator-meaningful figure.
	rootPath = "/"

	// egressProbeTarget is the egress probe's dial destination:
	// Cloudflare's 1.1.1.1:443 — an anycast address, so the dial is
	// geography-independent, and TCP 443 to a well-known public
	// resolver is exactly the traffic an egress-restricted network
	// blocks first. Success means generic HTTPS egress works.
	egressProbeTarget = "1.1.1.1:443"
	// egressProbeTimeout bounds the probe: a black-holed network must
	// delay the join by seconds, not minutes.
	egressProbeTimeout = 5 * time.Second
)

// readProcFile is a package variable so tests can script it (AD-25:
// no real /proc in unit tests).
var readProcFile = os.ReadFile

// egressDialer abstracts the probe's dial for tests; *net.Dialer is
// the production value.
type egressDialer interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
}

// machineCollector reads this host's facts through injectable sources.
type machineCollector struct {
	dialer egressDialer
	now    func() time.Time
}

// newMachineCollector wires the production collector.
func newMachineCollector() *machineCollector {
	return &machineCollector{
		dialer: &net.Dialer{Timeout: egressProbeTimeout},
		now:    time.Now,
	}
}

// collectFacts is the command's collection entry — a package variable
// so tests can pin the observed block without /proc, statfs, or any
// network (AD-25: the unit layer is network- and disk-free).
var collectFacts = func(ctx context.Context) *hostdomain.Capabilities {
	return newMachineCollector().collect(ctx)
}

// collect gathers every fact it can and returns the block to send.
// nil means nothing at all was observed — the payload then omits the
// capabilities key entirely (the old-CLI wire shape), which
// mixed-version topologyds accept.
func (c *machineCollector) collect(ctx context.Context) *hostdomain.Capabilities {
	var caps hostdomain.Capabilities
	if raw, err := readProcFile(cpuinfoPath); err == nil {
		caps.Hardware.CPUCores = parseCPUCores(raw)
	}
	if raw, err := readProcFile(meminfoPath); err == nil {
		caps.Hardware.MemoryMB = parseMemTotalMB(raw)
	}
	if arch := goarchToVocabulary(runtime.GOARCH); arch != "" {
		caps.Hardware.Arch = arch
	}
	if free, err := statfsFreeBytes(rootPath); err == nil {
		caps.Hardware.DiskGB = diskGB(free)
	}
	if egress := c.probeEgress(ctx); egress != nil {
		caps.Network.Egress = egress
	}
	if caps.Hardware == (hostdomain.HardwareCapabilities{}) && caps.Network.Egress == nil {
		return nil
	}
	caps.CollectedAt = c.now().UTC()
	return &caps
}

// probeEgress dials the well-known anycast endpoint. A network failure
// means no usable generic egress — the fact is false, never omitted.
// A canceled context means the probe could not run at all (the join
// itself is aborting); that absence stays unobserved rather than
// recording a false the network never earned.
func (c *machineCollector) probeEgress(ctx context.Context) *bool {
	conn, err := c.dialer.DialContext(ctx, "tcp", egressProbeTarget)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil
		}
		egress := false
		return &egress
	}
	_ = conn.Close()
	egress := true
	return &egress
}

// parseCPUCores counts logical CPUs from /proc/cpuinfo: one
// "processor\t: N" line per logical core. Absent or malformed content
// yields 0 — "not observed", the evaluator's missing-fact gap.
func parseCPUCores(cpuinfo []byte) int {
	cores := 0
	for _, line := range strings.Split(string(cpuinfo), "\n") {
		key, _, found := strings.Cut(line, ":")
		if found && strings.TrimSpace(key) == "processor" {
			cores++
		}
	}
	return cores
}

// parseMemTotalMB converts /proc/meminfo's MemTotal line (kB) to
// rounded MiB. Absent or malformed content yields 0.
func parseMemTotalMB(meminfo []byte) int {
	for _, line := range strings.Split(string(meminfo), "\n") {
		key, value, found := strings.Cut(line, ":")
		if !found || strings.TrimSpace(key) != "MemTotal" {
			continue
		}
		fields := strings.Fields(value)
		if len(fields) < 1 {
			return 0
		}
		kb, err := strconv.Atoi(fields[0])
		if err != nil {
			return 0
		}
		return (kb + 512) / 1024
	}
	return 0
}

// goarchToVocabulary maps the Go toolchain's GOARCH onto the advisor's
// arch vocabulary (advisor-l1 §2). Anything outside the vocabulary
// maps to "" — not observed — rather than inventing a value the
// evaluator would fail closed on anyway.
func goarchToVocabulary(goarch string) string {
	switch goarch {
	case "amd64":
		return "x86_64"
	case "arm64":
		return "arm64"
	default:
		return ""
	}
}

// diskGB converts available bytes to whole GiB, clamped to the wire
// validation's upper bound: the conversion stays a safe int and the
// observed value stays honest about floors either way (a disk larger
// than the clamp still passes every profile's minimum).
func diskGB(freeBytes uint64) int {
	gb := freeBytes / (1 << 30)
	const clamp = 1 << 22 // hostdomain's maxDiskGB sanity bound (4 PiB)
	if gb > clamp {
		return clamp
	}
	return int(gb)
}

// describeFacts renders the observed block for the join summary line —
// the operator sees exactly what the cluster will store.
func describeFacts(caps *hostdomain.Capabilities) string {
	if caps == nil {
		return "none observed (capabilities stay hand-declared)"
	}
	parts := []string{}
	hw := caps.Hardware
	if hw.CPUCores > 0 {
		parts = append(parts, fmt.Sprintf("cpu_cores=%d", hw.CPUCores))
	}
	if hw.MemoryMB > 0 {
		parts = append(parts, fmt.Sprintf("memory_mb=%d", hw.MemoryMB))
	}
	if hw.DiskGB > 0 {
		parts = append(parts, fmt.Sprintf("disk_gb=%d", hw.DiskGB))
	}
	if hw.Arch != "" {
		parts = append(parts, "arch="+hw.Arch)
	}
	if caps.Network.Egress != nil {
		parts = append(parts, fmt.Sprintf("egress=%t", *caps.Network.Egress))
	}
	if len(parts) == 0 {
		return "none observed (capabilities stay hand-declared)"
	}
	return strings.Join(parts, " ")
}

// printFactsSummary announces what was observed; w is the command's
// stdout.
func printFactsSummary(w io.Writer, caps *hostdomain.Capabilities) {
	_, _ = fmt.Fprintf(w, "observed facts: %s\n", describeFacts(caps))
}
