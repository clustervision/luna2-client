package v2

import (
	"encoding/json"
	"fmt"
	"sort"
)

// unknownKey scans a freshly-decoded JSON object for keys that are not in
// the allowed list, and returns the first unknown key in lexical order.
// The lexical sort is a deliberate deviation from the Bash validator's
// "first unsorted key" behaviour — there it was non-deterministic, here
// we want reproducible error messages for tests. The error string format
// otherwise matches lib/json_v2.sh::_v2_fail_unknown_keys verbatim.
func unknownKey(obj map[string]json.RawMessage, allowed []string, label string) error {
	if len(obj) == 0 {
		return nil
	}
	allow := make(map[string]struct{}, len(allowed))
	for _, k := range allowed {
		allow[k] = struct{}{}
	}
	var bad []string
	for k := range obj {
		if _, ok := allow[k]; !ok {
			bad = append(bad, k)
		}
	}
	if len(bad) == 0 {
		return nil
	}
	sort.Strings(bad)
	return fmt.Errorf("config_validation: %s: unknown field '%s'", label, bad[0])
}

// objectFromRaw decodes a JSON object value into map[string]RawMessage
// without surfacing a panic when the value is null or absent.
func objectFromRaw(raw json.RawMessage) (map[string]json.RawMessage, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	return m, nil
}
