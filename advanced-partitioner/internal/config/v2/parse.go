package v2

import (
	"encoding/json"
	"fmt"
	"os"
)

// LoadFile reads and decodes a v2 config from path. It does NOT validate;
// callers must call Validate on the result.
//
// LoadFile is the moral equivalent of json_validate_file + jq -c . in the
// Bash tree: it returns an error only if the file cannot be read or is not
// valid JSON. Schema validation is a separate step.
func LoadFile(path string) (*Config, []byte, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // operator-supplied config path; binary runs as root
	if err != nil {
		return nil, nil, fmt.Errorf("read %s: %w", path, err)
	}
	cfg, err := Parse(raw)
	if err != nil {
		return nil, raw, err
	}
	return cfg, raw, nil
}

// Parse decodes raw JSON into a Config. It does NOT validate.
//
// Decoding is tolerant of a sets field that is not an array: the typed
// Sets slice is left empty in that case so Validate can emit the
// "sets must be an array" error rather than a generic JSON unmarshal
// error. This mirrors what the jq-based Bash validator does naturally
// thanks to jq's dynamic typing.
func Parse(raw []byte) (*Config, error) {
	var loose map[string]json.RawMessage
	if err := json.Unmarshal(raw, &loose); err != nil {
		return nil, fmt.Errorf("config is not valid JSON: %w", err)
	}
	c := &Config{}
	if v, ok := loose["version"]; ok {
		_ = json.Unmarshal(v, &c.Version) //nolint:errcheck // Validate reports the bad value
	}
	if v, ok := loose["sets"]; ok && looksLikeJSONArray(v) {
		if err := json.Unmarshal(v, &c.Sets); err != nil {
			return nil, fmt.Errorf("config is not valid JSON: sets: %w", err)
		}
	}
	if v, ok := loose["comment"]; ok {
		_ = json.Unmarshal(v, &c.Comment) //nolint:errcheck // free-text; a non-string comment is caught by ValidateRaw's typed decode
	}
	return c, nil
}
