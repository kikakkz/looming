// SPDX-License-Identifier: Apache-2.0

package joincmd

import (
	"context"
	"errors"
	"net"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	hostdomain "github.com/kikakkz/looming/platform/go/hostdomain"
)

// fakeEgressDialer scripts the probe: dialing succeeds or fails.
type fakeEgressDialer struct {
	err error
}

func (f *fakeEgressDialer) DialContext(_ context.Context, _, address string) (net.Conn, error) {
	if address != egressProbeTarget {
		return nil, errors.New("unexpected probe target: " + address)
	}
	if f.err != nil {
		return nil, f.err
	}
	client, _ := net.Pipe()
	return client, nil
}

func scriptSources(t *testing.T, cpuinfo, meminfo []byte, cpuErr, memErr, diskErr error, freeBytes uint64) {
	t.Helper()
	origRead, origStat := readProcFile, statfsFreeBytes
	readProcFile = func(path string) ([]byte, error) {
		switch path {
		case cpuinfoPath:
			return cpuinfo, cpuErr
		case meminfoPath:
			return meminfo, memErr
		default:
			return nil, errors.New("unexpected proc path: " + path)
		}
	}
	statfsFreeBytes = func(path string) (uint64, error) {
		if path != rootPath {
			return 0, errors.New("unexpected statfs path: " + path)
		}
		return freeBytes, diskErr
	}
	t.Cleanup(func() {
		readProcFile = origRead
		statfsFreeBytes = origStat
	})
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	// #nosec G304 -- the path is this test's own testdata directory.
	raw, err := os.ReadFile("testdata/" + name)
	require.NoError(t, err)
	return raw
}

func TestParseCPUCores(t *testing.T) {
	assert.Equal(t, 2, parseCPUCores(fixture(t, "cpuinfo")))
	assert.Equal(t, 0, parseCPUCores(nil))
	assert.Equal(t, 0, parseCPUCores([]byte("nothing here\n")))
	assert.Equal(t, 1, parseCPUCores([]byte("processor\t: 0\nmodel name\t: x\n")))
	// A processor-shaped line that is not a cpu line must not count.
	assert.Equal(t, 0, parseCPUCores([]byte("myprocessor\t: 0\nprocessors\t: 2\n")))
}

func TestParseMemTotalMB(t *testing.T) {
	assert.Equal(t, 15955, parseMemTotalMB(fixture(t, "meminfo")))
	assert.Equal(t, 0, parseMemTotalMB(nil))
	assert.Equal(t, 0, parseMemTotalMB([]byte("MemAvailable: 123 kB\n")))
	assert.Equal(t, 0, parseMemTotalMB([]byte("MemTotal: lots kB\n")))
	assert.Equal(t, 1, parseMemTotalMB([]byte("MemTotal: 512 kB\n")), "rounding halves up")
}

func TestGoarchToVocabulary(t *testing.T) {
	assert.Equal(t, "x86_64", goarchToVocabulary("amd64"))
	assert.Equal(t, "arm64", goarchToVocabulary("arm64"))
	assert.Equal(t, "", goarchToVocabulary("riscv64"))
	assert.Equal(t, "", goarchToVocabulary(""))
}

// TestCollectGathersEveryFact is the happy path: all sources readable,
// the probe succeeds — the full block carries the collection stamp.
func TestCollectGathersEveryFact(t *testing.T) {
	scriptSources(t, fixture(t, "cpuinfo"), fixture(t, "meminfo"), nil, nil, nil, 500*(1<<30))
	collector := &machineCollector{
		dialer: &fakeEgressDialer{},
		now:    func() time.Time { return time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC) },
	}

	caps := collector.collect(context.Background())
	require.NotNil(t, caps)
	assert.Equal(t, 2, caps.Hardware.CPUCores)
	assert.Equal(t, 15955, caps.Hardware.MemoryMB)
	assert.Equal(t, 500, caps.Hardware.DiskGB)
	assert.NotEmpty(t, caps.Hardware.Arch, "the test host's GOARCH maps onto the vocabulary")
	require.NotNil(t, caps.Network.Egress)
	assert.True(t, *caps.Network.Egress)
	assert.True(t, caps.CollectedAt.Equal(time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)))
}

// TestCollectDegradesPerFact: every source failing independently
// drops only its fact; a failed probe reports egress false (blocked),
// not missing; and with nothing at all the block is nil (the payload
// omits the key — the old-CLI wire shape).
func TestCollectDegradesPerFact(t *testing.T) {
	boom := errors.New("no such file")
	scriptSources(t, nil, nil, boom, boom, boom, 0)
	collector := &machineCollector{
		dialer: &fakeEgressDialer{err: errors.New("connection refused")},
		now:    time.Now,
	}

	caps := collector.collect(context.Background())
	require.NotNil(t, caps)
	assert.Zero(t, caps.Hardware.CPUCores)
	assert.Zero(t, caps.Hardware.MemoryMB)
	assert.Zero(t, caps.Hardware.DiskGB)
	require.NotNil(t, caps.Network.Egress, "a failed probe is an observed false, never a gap")
	assert.False(t, *caps.Network.Egress)

	// Egress observed but nothing else: only the network fact (plus
	// the always-known arch) ships.
	caps = (&machineCollector{dialer: &fakeEgressDialer{}, now: time.Now}).collect(context.Background())
	require.NotNil(t, caps)
	assert.Zero(t, caps.Hardware.CPUCores)
	assert.NotNil(t, caps.Network.Egress)
}

func TestDescribeFacts(t *testing.T) {
	egress := false
	full := &hostdomain.Capabilities{
		Hardware:    hostdomain.HardwareCapabilities{CPUCores: 8, MemoryMB: 32768, DiskGB: 457, Arch: "x86_64"},
		Network:     hostdomain.NetworkCapabilities{Egress: &egress},
		CollectedAt: time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC),
	}
	assert.Equal(t, "cpu_cores=8 memory_mb=32768 disk_gb=457 arch=x86_64 egress=false", describeFacts(full))

	partial := &hostdomain.Capabilities{Hardware: hostdomain.HardwareCapabilities{CPUCores: 4}}
	assert.Equal(t, "cpu_cores=4", describeFacts(partial))

	assert.Equal(t, "none observed (capabilities stay hand-declared)", describeFacts(nil))
	assert.Equal(t, "none observed (capabilities stay hand-declared)", describeFacts(&hostdomain.Capabilities{}))
}
