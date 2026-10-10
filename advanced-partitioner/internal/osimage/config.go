package osimage

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/jsonstrict"
)

// FilterConfig is the JSON shape consumed by `lpart-osimage-install --config`.
// Pinned in Luna as osimage_filter:
//
//	{
//	  "preserve":       ["var/log/***", "boot/grub*/grub.cfg"],
//	  "preserve_empty": ["data", "home", "local", "tmp"],
//	  "no_new_files":   ["boot/efi", "cm/images", "proc"]
//	}
//
// sync/auto refresh (converge to the image) also read:
//
//	"keep_extra": ["var/log", "home"]      image files refreshed, files the image lacks kept
//	"image_wins": ["etc/ssh/sshd_config"]  taken from the image even inside a preserved tree
//	"verify":     "metadata" | "content" | "none"   (default metadata)
type FilterConfig struct {
	Preserve      []string `json:"preserve,omitempty"`
	PreserveEmpty []string `json:"preserve_empty,omitempty"`
	NoNewFiles    []string `json:"no_new_files,omitempty"`
	KeepExtra     []string `json:"keep_extra,omitempty"`
	ImageWins     []string `json:"image_wins,omitempty"`
	Verify        string   `json:"verify,omitempty"`
}

// ErrFilterConfig marks a filter config that is present but unusable; the
// caller treats it as a non-retryable configuration error.
type ErrFilterConfig struct{ Err error }

func (e *ErrFilterConfig) Error() string { return e.Err.Error() }
func (e *ErrFilterConfig) Unwrap() error { return e.Err }

// LoadFilterConfig reads and decodes a filter config file. Unknown keys are an
// error: a typo such as "preseve" used to be ignored silently, so the paths it
// meant to protect were deleted without a warning.
func LoadFilterConfig(path string) (*FilterConfig, error) {
	body, err := os.ReadFile(path) //nolint:gosec // operator-supplied config
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return &FilterConfig{}, nil
	}
	// one JSON object, no repeated keys: a second "preserve" used to replace
	// the first list silently, and data after the object was ignored
	if err := jsonstrict.Check(body); err != nil {
		return nil, &ErrFilterConfig{fmt.Errorf("osimage_filter: invalid (%w)", err)}
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	var c FilterConfig
	if err := dec.Decode(&c); err != nil {
		return nil, &ErrFilterConfig{fmt.Errorf("osimage_filter: invalid (%w); allowed keys: preserve, preserve_empty, no_new_files, keep_extra, image_wins, verify", err)}
	}
	switch c.Verify {
	case "", "metadata", "content", "none":
	default:
		return nil, &ErrFilterConfig{fmt.Errorf("osimage_filter: verify must be metadata, content or none, not %q", c.Verify)}
	}
	return &c, nil
}
