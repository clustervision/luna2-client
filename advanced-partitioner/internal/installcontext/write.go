package installcontext

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// WriteJSON writes the context as JSON to p with mode 0o640 (operator
// readable), creating parent directories as needed. The output shape
// is the canonical install-context.json: {"install_mode":"auto"}.
func WriteJSON(p string, ctx Context) error {
	if err := ctx.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(p), err)
	}
	body, err := json.MarshalIndent(ctx, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	body = append(body, '\n')
	if err := os.WriteFile(p, body, 0o640); err != nil { //nolint:gosec // operator-controlled path
		return fmt.Errorf("write %s: %w", p, err)
	}
	return nil
}
