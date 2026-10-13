// SPDX-License-Identifier: Apache-2.0

// Package buildinfo assembles the self-description block every Looming
// binary prints for --version (release-l1 §5 S1, AD-40): version,
// commit, dirty, build time, Go version, and platform — the
// goreleaser/promu shape.
//
// The metadata split is the contract (AD-40): build time is always the
// injected BUILD_TIME — the moment CI built the artifact — never Go's
// vcs.time, which is the commit time; rebuilding one commit must report
// a new build time while commit and dirty stay identical. An
// uninjected build time renders as "unknown", an absent commit as
// "none". Commit and dirty prefer
// the runtime/debug VCS stamps (vcs.revision/vcs.modified), which
// builds inside a .git checkout carry automatically; when the stamps
// are absent — CI builds without .git — the commit falls back to the
// injected value. Without VCS stamps there is no observable dirty
// state, so Dirty then reports false.
package buildinfo

import (
	"flag"
	"fmt"
	"io"
	"runtime"
	"runtime/debug"
)

// Link-time injection targets (S3 wires the Dockerfiles and the
// release CI, release-l1 §5):
//
//	-X github.com/kikakkz/looming/platform/go/buildinfo.version=v0.1.0
//	-X github.com/kikakkz/looming/platform/go/buildinfo.commit=<sha>
//	-X github.com/kikakkz/looming/platform/go/buildinfo.buildTime=<RFC3339>
var (
	version   = "dev"
	commit    = ""
	buildTime = ""
)

// BuildInfo is one binary's self-description block — the fields the
// promu-shaped String renders.
type BuildInfo struct {
	Version   string
	Commit    string
	Dirty     bool
	BuildTime string
	GoVersion string
	Platform  string
}

// readVCS is the VCS-stamp seam: production reads the
// runtime/debug stamps; tests substitute either path. ok is false when
// the binary carries no vcs.revision stamp.
var readVCS = func() (rev string, modified bool, ok bool) {
	info, found := debug.ReadBuildInfo()
	if !found {
		return "", false, false
	}
	var revision string
	var dirty bool
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			revision = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if revision == "" {
		return "", false, false
	}
	return revision, dirty, true
}

// Get assembles the block from the injected values, the VCS stamps,
// and the runtime. Commit and dirty come from the VCS stamps when
// present, else from the injected commit; an uninjected build time
// renders as "unknown".
func Get() BuildInfo {
	b := BuildInfo{
		Version:   version,
		BuildTime: buildTime,
		GoVersion: runtime.Version(),
		Platform:  runtime.GOOS + "/" + runtime.GOARCH,
	}
	if rev, modified, ok := readVCS(); ok {
		b.Commit = rev
		b.Dirty = modified
	} else {
		b.Commit = commit
	}
	if b.BuildTime == "" {
		b.BuildTime = "unknown"
	}
	return b
}

// String renders the promu-shaped version block headed by the binary's
// name; the caller supplies it because BuildInfo carries no name. An
// absent commit (no VCS stamps, nothing injected) renders as "none".
func (b BuildInfo) String(name string) string {
	rev := b.Commit
	if rev == "" {
		rev = "none"
	}
	return fmt.Sprintf("%s version %s\n  commit: %s (dirty: %t)\n  build time: %s\n  go: %s\n  platform: %s",
		name, b.Version, shortCommit(rev), b.Dirty, b.BuildTime, b.GoVersion, b.Platform)
}

// shortLen is the promu/goreleaser short-commit width.
const shortLen = 7

func shortCommit(c string) string {
	if len(c) > shortLen {
		return c[:shortLen]
	}
	return c
}

// HandleVersionFlag is the --version wiring shared by the non-cobra
// daemon mains (gateway, identityd, topologyd): it scans args for
// -version/--version and, when set, prints name's block to w and
// returns true so main can exit 0 before any other wiring. Anything
// else returns false — an unknown or malformed flag is not a version
// request, and the caller's normal boot reports its own config errors.
func HandleVersionFlag(name string, args []string, w io.Writer) bool {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	show := fs.Bool("version", false, "print version and exit")
	if err := fs.Parse(args); err != nil {
		return false
	}
	if !*show {
		return false
	}
	// A stdout write failure cannot be acted on at this point; main is
	// about to exit 0 either way.
	_, _ = fmt.Fprintln(w, Get().String(name))
	return true
}
