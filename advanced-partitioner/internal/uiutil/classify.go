// Package uiutil holds layout/truncate/format/colour helpers and the
// output-line classifier. The classifier is render-only: it never
// mutates, drops, or reorders lines.
package uiutil

import (
	"strings"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/model"
)

// ClassifyInput is the set of inputs the classifier considers. Stream
// is from the engine event (stdout/stderr/command/system/discovery);
// EventType is the precise event source (raw.stdout, command.started,
// etc.). When Force is non-empty it short-circuits all detection — used
// by the engine for synthetic stage banners.
type ClassifyInput struct {
	Line   string
	Stream string
	Force  model.LineClass
}

// Classify picks a LineClass per the precedence in tui-design.plan
// §"Output colouring/classification":
//
//  1. error  2. destructive  3. warning  4. command
//  5. marker/stage  6. success  7. stderr  8. normal
func Classify(in ClassifyInput) model.LineClass {
	if in.Force != "" {
		return in.Force
	}
	raw := in.Line
	low := strings.ToLower(raw)

	if isError(low) {
		return model.ClassError
	}
	if isDestructive(low) {
		return model.ClassDestructive
	}
	if isWarning(low) {
		return model.ClassWarning
	}
	if isCommandLine(raw) {
		return model.ClassCommand
	}
	if isMarker(raw) {
		return model.ClassMarker
	}
	if isSuccess(low) {
		return model.ClassSuccess
	}
	if in.Stream == "stderr" {
		return model.ClassStderr
	}
	return model.ClassNormal
}

func isError(low string) bool {
	for _, k := range []string{"error", "failed", "fatal", "panic", "denied", "not found"} {
		if strings.Contains(low, k) {
			return true
		}
	}
	return false
}

// destructiveKeywords are the command names and phrases that flag a
// storage-mutating action. Match on word-ish boundary so "mkfs" alone
// triggers but a word containing "format" still does (logs commonly say
// "creating filesystem format=xfs").
var destructiveKeywords = []string{
	"sgdisk", "parted", "wipefs", "blkdiscard", " dd ", "mkfs",
	"pvcreate", "vgcreate", "lvcreate", "grub-install", "efibootmgr",
	"writing partition table", "creating gpt", "wiping signature",
	"format ",
}

func isDestructive(low string) bool {
	for _, k := range destructiveKeywords {
		if strings.Contains(low, k) {
			return true
		}
	}
	return false
}

func isWarning(low string) bool {
	for _, k := range []string{"warn", "warning", "skipped", "retry", "deprecated"} {
		if strings.Contains(low, k) {
			return true
		}
	}
	return false
}

// isCommandLine matches Bash xtrace ("+ cmd args") and our own command
// banner ("$ cmd args").
func isCommandLine(raw string) bool {
	s := strings.TrimLeft(raw, " \t")
	if strings.HasPrefix(s, "$ ") {
		return true
	}
	if strings.HasPrefix(s, "+") {
		i := 0
		for i < len(s) && s[i] == '+' {
			i++
		}
		return i < len(s) && s[i] == ' '
	}
	return false
}

func isMarker(raw string) bool {
	s := strings.TrimLeft(raw, " \t")
	return strings.HasPrefix(s, "LPART_")
}

func isSuccess(low string) bool {
	for _, k := range []string{" ok ", " ok\t", " ok:", "done", "success", "completed", "created", "mounted", "synced", "installed"} {
		if strings.Contains(low, k) {
			return true
		}
	}
	// "OK" at the very start or end of a line is also a success signal.
	t := strings.TrimSpace(low)
	if t == "ok" || strings.HasPrefix(t, "ok ") || strings.HasSuffix(t, " ok") {
		return true
	}
	return false
}

// ContainsDevicePath returns true if the line mentions a device-like
// path. Used by the renderer to add subtle emphasis on top of whatever
// LineClass was chosen.
func ContainsDevicePath(line string) bool {
	for _, k := range []string{"/dev/nvme", "/dev/sd", "/dev/mapper/", "/dev/disk/by-id/", "/dev/disk/by-path/", "tmpfs", "/mnt/"} {
		if strings.Contains(line, k) {
			return true
		}
	}
	return false
}
