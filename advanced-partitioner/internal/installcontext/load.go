package installcontext

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
)

// DefaultJSONPath is the canonical location for install-context.json.
// Matches the partscript layout under /tmp/install_files/runtime/.
const DefaultJSONPath = "/tmp/install_files/runtime/install-context.json"

// LoadOptions controls Load(). Empty fields fall back to the documented
// defaults; all fields are overridable for testing.
type LoadOptions struct {
	// ExplicitMode, when non-empty, takes top priority regardless of any
	// other source. Used by tools that accept an `--install-mode` CLI flag.
	ExplicitMode Mode

	// JSONPath is the path to install-context.json. Defaults to
	// DefaultJSONPath. A missing file is not an error — the loader simply
	// falls through to the defaults.
	JSONPath string
}

// resolvedPath returns p if non-empty, else def.
func resolvedPath(p, def string) string {
	if p == "" {
		return def
	}
	return p
}

// Load resolves an install context with precedence: explicit arg > JSON file
// > default (install_mode=auto). On success the returned Context has a valid
// InstallMode.
func Load(opts LoadOptions) (Context, error) {
	ctx := Defaults()

	jsonPath := resolvedPath(opts.JSONPath, DefaultJSONPath)
	if body, err := os.ReadFile(jsonPath); err == nil { //nolint:gosec // operator-controlled path
		var fileCtx Context
		if err := json.Unmarshal(body, &fileCtx); err != nil {
			return ctx, fmt.Errorf("parse %s: %w", jsonPath, err)
		}
		if fileCtx.InstallMode != "" {
			ctx.InstallMode, ctx.Source = fileCtx.InstallMode, SourceFile
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return ctx, fmt.Errorf("read %s: %w", jsonPath, err)
	}

	if opts.ExplicitMode != "" {
		ctx.InstallMode, ctx.Source = opts.ExplicitMode, SourceExplicit
	}

	if err := ctx.Validate(); err != nil {
		return ctx, err
	}
	return ctx, nil
}

// LoadFile reads the JSON file at p. A missing file returns Defaults() with
// no error.
func LoadFile(p string) (Context, error) {
	body, err := os.ReadFile(p) //nolint:gosec // operator-controlled path
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Defaults(), nil
		}
		return Context{}, fmt.Errorf("read %s: %w", p, err)
	}
	var ctx Context
	if err := json.Unmarshal(body, &ctx); err != nil {
		return Context{}, fmt.Errorf("parse %s: %w", p, err)
	}
	if ctx.InstallMode == "" {
		ctx.InstallMode = ModeAuto
	}
	if ctx.Source == "" {
		ctx.Source = SourceFile
	}
	if err := ctx.Validate(); err != nil {
		return Context{}, err
	}
	return ctx, nil
}
