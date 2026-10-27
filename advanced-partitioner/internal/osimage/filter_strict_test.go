package osimage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A repeated key used to replace the first list silently; trailing data was
// ignored. Both are configuration errors now.
func TestLoadFilterConfigStrictDocument(t *testing.T) {
	dir := t.TempDir()
	for name, doc := range map[string]string{
		"dup":      `{"preserve":["var/log"],"preserve":null}`,
		"case":     `{"preserve":["var/log"],"PRESERVE":["home"]}`,
		"trailing": `{"preserve":["var/log"]} {"preserve":[]}`,
	} {
		p := filepath.Join(dir, name+".json")
		_ = os.WriteFile(p, []byte(doc), 0o600)
		_, err := LoadFilterConfig(p)
		var fe *ErrFilterConfig
		if err == nil || !strings.Contains(err.Error(), "osimage_filter: invalid") || !asFilterErr(err, &fe) {
			t.Errorf("%s: want a filter config error, got %v", name, err)
		}
	}
	p := filepath.Join(dir, "ok.json")
	_ = os.WriteFile(p, []byte(`{"preserve":["var/log"],"keep_extra":["home"]}`), 0o600)
	if _, err := LoadFilterConfig(p); err != nil {
		t.Fatalf("valid filter refused: %v", err)
	}
}

func asFilterErr(err error, target **ErrFilterConfig) bool {
	e, ok := err.(*ErrFilterConfig)
	if ok {
		*target = e
	}
	return ok
}
