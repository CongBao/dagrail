// Package bundle exposes the host-neutral DAGrail plugin projection that is
// linked into every release binary. The installer materializes these exact
// bytes; agent hosts never need to fetch a moving branch to obtain skills,
// hooks, manifests, or brand assets.
package bundle

import "embed"

// PluginFS is deliberately limited to public plugin material. Runtime state,
// repository metadata, source code, and release credentials are not embedded.
//
//go:embed all:.agents all:.claude-plugin all:.codex-plugin all:.github/plugin all:.plugin all:assets all:hooks all:skills
var PluginFS embed.FS
