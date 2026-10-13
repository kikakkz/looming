// SPDX-License-Identifier: Apache-2.0

package buildinfo

import (
	"bytes"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// vcsFunc is the readVCS seam's shape, named for the stub helpers.
type vcsFunc func() (rev string, modified bool, ok bool)

// stub installs the link-time variables and the VCS seam for one test,
// restoring both on cleanup. Every test stubs readVCS: the real stamp
// set of the test binary depends on how `go test` was invoked (inside
// a .git checkout or not), which no assertion may rely on.
func stub(t *testing.T, ver, com, bt string, vcs vcsFunc) {
	t.Helper()
	origVersion, origCommit, origTime, origRead := version, commit, buildTime, readVCS
	version, commit, buildTime, readVCS = ver, com, bt, vcs
	t.Cleanup(func() {
		version, commit, buildTime, readVCS = origVersion, origCommit, origTime, origRead
	})
}

func vcsAbsent() vcsFunc {
	return func() (string, bool, bool) { return "", false, false }
}

func vcsPresent(rev string, dirty bool) vcsFunc {
	return func() (string, bool, bool) { return rev, dirty, true }
}

// TestGetDevDefaults pins the uninjected shape: version falls back to
// "dev", the missing build time reports "unknown", and without VCS
// stamps there is no commit and no dirty state.
func TestGetDevDefaults(t *testing.T) {
	// "dev" is the link-time default of the version variable; the other
	// two stay uninjected.
	stub(t, "dev", "", "", vcsAbsent())

	b := Get()
	assert.Equal(t, "dev", b.Version)
	assert.Empty(t, b.Commit)
	assert.False(t, b.Dirty)
	assert.Equal(t, "unknown", b.BuildTime)
	assert.Equal(t, runtime.Version(), b.GoVersion)
	assert.Equal(t, runtime.GOOS+"/"+runtime.GOARCH, b.Platform)
}

// TestGetInjectedFallback pins the CI-no-.git path: with no VCS stamps
// the injected values carry the block.
func TestGetInjectedFallback(t *testing.T) {
	stub(t, "v0.1.0", "1c5f71f9deadbeef", "2026-10-13T01:23:45Z", vcsAbsent())

	b := Get()
	assert.Equal(t, "v0.1.0", b.Version)
	assert.Equal(t, "1c5f71f9deadbeef", b.Commit)
	assert.False(t, b.Dirty)
	assert.Equal(t, "2026-10-13T01:23:45Z", b.BuildTime)
}

// TestGetVCSPreferred pins the local-build path: the runtime/debug
// stamps win over the injected commit, and vcs.modified surfaces as
// Dirty. Version and build time stay injected — they are never VCS
// data (AD-40: vcs.time is the commit time, never the build time).
func TestGetVCSPreferred(t *testing.T) {
	stub(t, "v0.2.0", "deadbeefdeadbeef", "2026-10-14T10:00:00Z",
		vcsPresent("1c5f71f9abc", true))

	b := Get()
	assert.Equal(t, "1c5f71f9abc", b.Commit)
	assert.True(t, b.Dirty)
	assert.Equal(t, "v0.2.0", b.Version)
	assert.Equal(t, "2026-10-14T10:00:00Z", b.BuildTime)
}

// TestMetadataSplit is AD-40's acceptance test: rebuilding one commit
// changes the build time only — commit and dirty must be identical
// across builds, on both metadata paths (VCS stamps and injected
// fallback).
func TestMetadataSplit(t *testing.T) {
	t.Run("vcs stamps", func(t *testing.T) {
		stub(t, "v0.1.0", "", "", vcsPresent("1c5f71f9abc", false))
		buildTime = "2026-10-13T01:23:45Z"
		first := Get()
		buildTime = "2026-10-13T02:23:45Z"
		second := Get()

		assert.Equal(t, first.Commit, second.Commit)
		assert.Equal(t, first.Dirty, second.Dirty)
		assert.Equal(t, first.Version, second.Version)
		assert.NotEqual(t, first.BuildTime, second.BuildTime)
		assert.Equal(t, "2026-10-13T02:23:45Z", second.BuildTime)
	})

	t.Run("injected fallback", func(t *testing.T) {
		stub(t, "v0.1.0", "1c5f71f9abc", "", vcsAbsent())
		buildTime = "2026-10-13T01:23:45Z"
		first := Get()
		buildTime = "2026-10-13T02:23:45Z"
		second := Get()

		assert.Equal(t, first.Commit, second.Commit)
		assert.Equal(t, first.Dirty, second.Dirty)
		assert.Equal(t, first.Version, second.Version)
		assert.NotEqual(t, first.BuildTime, second.BuildTime)
	})
}

// TestString pins the promu-shaped block: the binary name heads it, the
// commit renders short, and every field lands on its own line.
func TestString(t *testing.T) {
	b := BuildInfo{
		Version:   "v0.2.0",
		Commit:    "1c5f71f9deadbeef",
		Dirty:     false,
		BuildTime: "2026-10-13T01:23:45Z",
		GoVersion: "go1.25.0",
		Platform:  "linux/amd64",
	}
	want := "looming version v0.2.0\n" +
		"  commit: 1c5f71f (dirty: false)\n" +
		"  build time: 2026-10-13T01:23:45Z\n" +
		"  go: go1.25.0\n" +
		"  platform: linux/amd64"
	assert.Equal(t, want, b.String("looming"))

	dirty := b
	dirty.Dirty = true
	assert.Contains(t, dirty.String("gateway"), "  commit: 1c5f71f (dirty: true)")

	empty := b
	empty.Commit = ""
	assert.Contains(t, empty.String("looming"), "  commit: none (dirty: false)")
}

// TestStringShortCommit pins the truncation boundary: seven characters
// is already short enough, eight is not.
func TestStringShortCommit(t *testing.T) {
	assert.Equal(t, "1c5f71", shortCommit("1c5f71"))
	assert.Equal(t, "1c5f71f", shortCommit("1c5f71f"))
	assert.Equal(t, "1c5f71f", shortCommit("1c5f71f9abc"))
	assert.Equal(t, "", shortCommit(""))
}

// TestHandleVersionFlag pins the non-cobra --version wiring: the flag
// prints the block and reports handled; anything else falls through so
// the daemon's normal boot (and its own config errors) stands.
func TestHandleVersionFlag(t *testing.T) {
	stub(t, "v9.9.9", "1c5f71f9abc", "2026-10-14T00:00:00Z", vcsAbsent())

	t.Run("long flag prints the block", func(t *testing.T) {
		var out bytes.Buffer
		require.True(t, HandleVersionFlag("identityd", []string{"--version"}, &out))
		assert.Contains(t, out.String(), "identityd version v9.9.9\n")
		assert.Contains(t, out.String(), "  commit: 1c5f71f")
		assert.Contains(t, out.String(), "  build time: 2026-10-14T00:00:00Z")
	})

	t.Run("single-dash flag prints too", func(t *testing.T) {
		var out bytes.Buffer
		require.True(t, HandleVersionFlag("gateway", []string{"-version"}, &out))
		assert.Contains(t, out.String(), "gateway version v9.9.9")
	})

	t.Run("no flag falls through", func(t *testing.T) {
		var out bytes.Buffer
		require.False(t, HandleVersionFlag("topologyd", nil, &out))
		assert.Empty(t, out.String())
	})

	t.Run("unrelated args fall through", func(t *testing.T) {
		var out bytes.Buffer
		require.False(t, HandleVersionFlag("topologyd", []string{"--listen", ":9090"}, &out))
		assert.Empty(t, out.String())
	})

	t.Run("unknown flag falls through without handling", func(t *testing.T) {
		var out bytes.Buffer
		require.False(t, HandleVersionFlag("topologyd", []string{"--bogus"}, &out))
		assert.Empty(t, out.String())
	})
}
