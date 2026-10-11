package main

import (
	"strings"
	"testing"

	v2 "gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/config/v2"
)

// A persistent set that does not mount parks in every mode, install_mode
// full included; the hint must not send the operator to install_mode full.
func TestMountExistingAdviceForPersistentSet(t *testing.T) {
	p := mountExistingAdvice(&v2.Set{Name: "data", Role: "data", Persistent: true})
	if strings.Contains(p, "install_mode full rebuilds") || !strings.Contains(p, "never repartitioned or formatted") {
		t.Fatalf("persistent advice: %s", p)
	}
	n := mountExistingAdvice(&v2.Set{Name: "os", Role: "os"})
	if !strings.Contains(n, "install_mode full rebuilds the set") {
		t.Fatalf("non-persistent advice: %s", n)
	}
}
