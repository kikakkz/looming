// Command gateway wires the slice-0 skeleton. Real serving lands with
// the forwarding slice; this main exists so the hexagon has an entry
// point and manual DI stays the pattern (no frameworks).
package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/kikakkz/looming/internal/control/app"
	defaultengine "github.com/kikakkz/looming/internal/engine/default"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	ctx := context.Background()

	_ = app.NewModelAllowlistCache(ctx) // projection cache, writer loop starts
	_ = defaultengine.New()             // engine slot's default fill
	log.Info("gateway slice-0 skeleton wired (no serving yet)")
}
