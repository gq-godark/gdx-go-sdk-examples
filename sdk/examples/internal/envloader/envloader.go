// Package envloader provides a tiny standard-library-only `.env` loader
// shared by the example main packages, plus a pretty-printer for
// godark.OrderError so MMs can see the symbolic reject reason (e.g.
// PRICE_DEVIATION_TOO_LARGE) at a glance.
//
// LoadDotenv reads a `.env` file from one of:
//
//  1. the directory of the currently running executable (handy when you've
//     built one of the examples into a binary and run it from elsewhere);
//  2. the current working directory (so `go run ./examples/quickstart`
//     from the repo root just works).
//
// The OS environment always wins over the file -- this matches the
// behaviour of the standard `github.com/joho/godotenv` library and keeps
// CI overrides (env vars exported by the workflow) authoritative.
package envloader

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/gq-godark/gdx-go-sdk"
)

// LoadDotenv applies the first `.env` file it finds adjacent to the
// running executable or in the current working directory. Missing files
// are silent no-ops; this is intentional so example binaries still run in
// environments (CI, containers) where credentials are injected directly.
func LoadDotenv() {
	candidates := []string{}
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), ".env"))
	}
	if wd, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(wd, ".env"))
	}
	for _, path := range candidates {
		if applyEnvFile(path) {
			return
		}
	}
}

func applyEnvFile(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		val := strings.TrimSpace(line[eq+1:])
		if (strings.HasPrefix(val, "\"") && strings.HasSuffix(val, "\"")) ||
			(strings.HasPrefix(val, "'") && strings.HasSuffix(val, "'")) {
			val = val[1 : len(val)-1]
		}
		if key == "" {
			continue
		}
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		_ = os.Setenv(key, val)
	}
	return true
}

// PrintOrderError formats a godark.OrderError so the symbolic reject reason
// (when the SDK was able to canonicalise it) is the first thing the operator
// sees. For non-OrderError errors it falls back to the default formatter.
func PrintOrderError(operation string, err error) {
	var oe *godark.OrderError
	if errors.As(err, &oe) {
		code := oe.ErrorCode
		if code == "" {
			code = "<none>"
		}
		fmt.Fprintf(os.Stderr, "%s: OrderError code=%s reason=%s\n",
			operation, code, oe.Error())
		return
	}
	fmt.Fprintf(os.Stderr, "%s: %v\n", operation, err)
}
