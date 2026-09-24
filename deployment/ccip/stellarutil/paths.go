package stellarutil

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// stellarRootModule is the module path of the chainlink-stellar root module.
// Sub-modules (tests/, deployment/) carry a path suffix (…/tests, …/deployment)
// and must not be mistaken for the repo root.
const stellarRootModule = "github.com/smartcontractkit/chainlink-stellar"

// FindStellarRoot locates the chainlink-stellar project root — the directory whose
// go.mod declares module github.com/smartcontractkit/chainlink-stellar — first via
// the CHAINLINK_STELLAR_ROOT environment variable, then by walking up from CWD.
// Matching by module path (rather than just "any go.mod") is required
// because tests/ and deployment/ are their own Go modules; a naive go.mod search
// started from tests/ would stop there and return the wrong directory.
//
// The env override lets callers outside the repo (e.g. a chainlink-deployments
// checkout) point at a chainlink-stellar checkout holding the release WASMs, for
// parity with deployment/mcmsutil. The walk-up works whether the devenv CLI is run
// from the repo root directly or from a subdirectory (e.g. `cd tests && …`).
func FindStellarRoot() (string, error) {
	if root := os.Getenv("CHAINLINK_STELLAR_ROOT"); root != "" {
		if mp, ok := goModModulePath(root); ok && mp == stellarRootModule {
			return root, nil
		}
		return "", fmt.Errorf("CHAINLINK_STELLAR_ROOT %s does not declare module %s", root, stellarRootModule)
	}
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if mp, ok := goModModulePath(dir); ok && mp == stellarRootModule {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("could not find chainlink-stellar root (module %s) in any parent of %s", stellarRootModule, dir)
		}
		dir = parent
	}
}

// goModModulePath reads dir/go.mod and returns its declared module path. It scans
// only the module directive; the rest of the file is ignored. ok is false if go.mod
// is missing or declares no module directive.
func goModModulePath(dir string) (string, bool) {
	b, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		return "", false
	}
	for _, raw := range strings.Split(string(b), "\n") {
		fields := strings.Fields(raw)
		if len(fields) >= 2 && fields[0] == "module" {
			return fields[1], true
		}
	}
	return "", false
}
