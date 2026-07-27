package osimage

import (
	"encoding/json"
	"fmt"
	"os"
)

// FilterConfig is the JSON shape consumed by `lpart-osimage-install --config`.
// Pinned in Luna as osimage_filter:
//
//	{
//	  "preserve":       ["var/log/***", "boot/grub*/grub.cfg"],
//	  "preserve_empty": ["data", "home", "local", "tmp"],
//	  "no_new_files":   ["boot/efi", "cm/images", "proc"]
//	}
type FilterConfig struct {
	Preserve      []string `json:"preserve,omitempty"`
	PreserveEmpty []string `json:"preserve_empty,omitempty"`
	NoNewFiles    []string `json:"no_new_files,omitempty"`
}

// LoadFilterConfig reads and decodes a filter config file.
func LoadFilterConfig(path string) (*FilterConfig, error) {
	body, err := os.ReadFile(path) //nolint:gosec // operator-supplied config
	if err != nil {
		return nil, err
	}
	var c FilterConfig
	if err := json.Unmarshal(body, &c); err != nil {
		return nil, fmt.Errorf("config: invalid JSON: %w", err)
	}
	return &c, nil
}
