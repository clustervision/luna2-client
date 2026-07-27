package discover

import (
	"fmt"
	"strings"
)

// String returns a single-line representation useful for tests and
// log records.
func (d Device) String() string {
	parts := []string{
		fmt.Sprintf("path=%s", d.Path),
		fmt.Sprintf("kernel=%s", d.KernelPath),
		fmt.Sprintf("size=%d", d.Size),
		fmt.Sprintf("media=%s", MediaLabel(d.Rotational)),
		fmt.Sprintf("tran=%s", TransportLabel(d.Name, d.Transport)),
	}
	if d.ByPathMissing {
		parts = append(parts, "by_path_missing=true")
	}
	return strings.Join(parts, " ")
}
