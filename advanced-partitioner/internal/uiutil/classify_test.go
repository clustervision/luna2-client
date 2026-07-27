package uiutil_test

import (
	"testing"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/model"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/uiutil"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		name   string
		line   string
		stream string
		force  model.LineClass
		want   model.LineClass
	}{
		// Precedence: error beats everything else.
		{"error beats destructive", "error: sgdisk failed on /dev/nvme0n1", "stdout", "", model.ClassError},
		{"failed token", "stage finalise failed", "stdout", "", model.ClassError},
		{"panic token", "runtime panic in dracut", "stderr", "", model.ClassError},

		// Destructive beats warning/command/marker.
		{"sgdisk plain", "sgdisk --zap-all /dev/sda", "stdout", "", model.ClassDestructive},
		{"mkfs plain", "mkfs.xfs -f /dev/mapper/x", "stdout", "", model.ClassDestructive},
		{"wipefs", "wipefs -a /dev/sda", "stdout", "", model.ClassDestructive},
		{"grub-install", "grub-install --target=x86_64-efi /dev/sda", "stdout", "", model.ClassDestructive},
		{"writing partition table", "writing partition table to /dev/sda", "stdout", "", model.ClassDestructive},

		// Warning.
		{"warning prefix", "warning: tmpfs not available", "stdout", "", model.ClassWarning},
		{"skipped", "skipped: legacy hosts file", "stdout", "", model.ClassWarning},
		{"deprecated", "deprecated: use --target=auto", "stdout", "", model.ClassWarning},

		// Command markers.
		{"dollar prefix", "$ lpart-discover --json /run/lpart-installer/discovery.json", "stdout", "", model.ClassCommand},
		{"bash xtrace", "+ mount -t tmpfs tmpfs /mnt/osimg", "stdout", "", model.ClassCommand},
		{"bash xtrace deep", "+++ mount -t tmpfs tmpfs /mnt/osimg", "stdout", "", model.ClassCommand},

		// Marker.
		{"TX marker", "LPART_STAGE=partition", "stdout", "", model.ClassMarker},
		{"TX marker indented", "   LPART_TARGET=tmpfs", "stdout", "", model.ClassMarker},

		// Success — but only when it does not also look destructive.
		{"mounted", "mounted tmpfs at /mnt/osimg", "stdout", "", model.ClassSuccess},
		{"installed", "rpm installed: kernel-5.14", "stdout", "", model.ClassSuccess},

		// Stderr fallthrough.
		{"plain stderr", "some unclassified noise", "stderr", "", model.ClassStderr},

		// Normal.
		{"normal", "extracting tarball to /sysroot", "stdout", "", model.ClassNormal},

		// Force overrides everything.
		{"force stage", "─── partition ───", "system", model.ClassStage, model.ClassStage},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := uiutil.Classify(uiutil.ClassifyInput{
				Line: tc.line, Stream: tc.stream, Force: tc.force,
			})
			if got != tc.want {
				t.Fatalf("Classify(%q stream=%s) = %q, want %q", tc.line, tc.stream, got, tc.want)
			}
		})
	}
}

func TestContainsDevicePath(t *testing.T) {
	yes := []string{
		"mount /dev/nvme0n1 at /mnt/x",
		"writing /dev/sda1",
		"resolved /dev/mapper/lpart-root",
		"/dev/disk/by-id/wwn-0x500a",
		"tmpfs mounted at /mnt/osimg",
	}
	no := []string{
		"all systems nominal",
		"deprecated: use --target=auto",
		"LPART_STAGE=partition",
	}
	for _, l := range yes {
		if !uiutil.ContainsDevicePath(l) {
			t.Errorf("expected device path in %q", l)
		}
	}
	for _, l := range no {
		if uiutil.ContainsDevicePath(l) {
			t.Errorf("did not expect device path in %q", l)
		}
	}
}
