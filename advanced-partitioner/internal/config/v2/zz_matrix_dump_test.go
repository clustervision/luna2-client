package v2

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func TestZZMatrixDump(t *testing.T) {
	dir := os.Getenv("MX_LAYOUTS")
	if dir == "" {
		t.Skip()
	}
	files, _ := filepath.Glob(filepath.Join(dir, "l*.json"))
	sort.Strings(files)
	for _, f := range files {
		raw, _ := os.ReadFile(f)
		res := "OK"
		cfg, err := Parse(raw)
		if err == nil {
			err = cfg.ValidateRaw(raw)
		}
		if err != nil {
			res = "REFUSED " + err.Error()
		}
		fmt.Printf("%s\t%s\n", filepath.Base(f), res)
	}
}
