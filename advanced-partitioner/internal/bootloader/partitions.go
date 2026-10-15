package bootloader

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/executor"
)

// gptTypeBIOSBoot is the BIOS boot partition type (EF02) as lsblk PARTTYPE /
// blkid PART_ENTRY_TYPE print it.
const gptTypeBIOSBoot = "21686148-6449-6e6f-744e-656564454649"

// blockDev is one line of `lsblk -nPp -o NAME,TYPE,PARTTYPE`.
type blockDev struct {
	Name, Type, PartType string
}

var lsblkPairRE = regexp.MustCompile(`([A-Z:_-]+)="((?:[^"\\]|\\.)*)"`)

// listBlockDevs lists disk and everything below it (partitions, and the md /
// LVM devices built on them). lsblk is read-only, so it also runs in dry-run.
// util-linux 2.32 (el8) has every column used here.
func listBlockDevs(ctx context.Context, runner executor.Runner, disk string) ([]blockDev, error) {
	res, err := runner.Run(ctx, executor.CommandSpec{
		Path:          "lsblk",
		Args:          []string{"-nPp", "-o", "NAME,TYPE,PARTTYPE", disk},
		CaptureStdout: true, CaptureStderr: true, Quiet: true,
	})
	if err != nil {
		return nil, fmt.Errorf("lsblk %s: %w", disk, err)
	}
	var out []blockDev
	for _, ln := range strings.Split(string(res.Stdout), "\n") {
		kv := map[string]string{}
		for _, m := range lsblkPairRE.FindAllStringSubmatch(ln, -1) {
			kv[m[1]] = unescapeLsblk(m[2])
		}
		if kv["NAME"] == "" {
			continue
		}
		out = append(out, blockDev{Name: kv["NAME"], Type: kv["TYPE"], PartType: strings.ToLower(kv["PARTTYPE"])})
	}
	// Without a udev database lsblk can leave PARTTYPE empty; probe those
	// partitions directly.
	for i := range out {
		if out[i].Type == "part" && out[i].PartType == "" {
			if r, err := runner.Run(ctx, executor.CommandSpec{
				Path:          "blkid",
				Args:          []string{"-p", "-s", "PART_ENTRY_TYPE", "-o", "value", out[i].Name},
				CaptureStdout: true, CaptureStderr: true, Quiet: true,
			}); err == nil {
				out[i].PartType = strings.ToLower(strings.TrimSpace(string(r.Stdout)))
			}
		}
	}
	return out, nil
}

// unescapeLsblk undoes lsblk's \xNN escaping in -P output.
func unescapeLsblk(s string) string {
	if !strings.Contains(s, `\x`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) && s[i+1] == 'x' {
			if v, err := strconv.ParseUint(s[i+2:i+4], 16, 8); err == nil {
				b.WriteByte(byte(v))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// biosBootPartitions returns the BIOS boot (EF02) partitions on disk.
func biosBootPartitions(ctx context.Context, runner executor.Runner, disk string) ([]blockDev, error) {
	devs, err := listBlockDevs(ctx, runner, disk)
	if err != nil {
		return nil, err
	}
	var out []blockDev
	for _, d := range devs {
		if d.Type == "part" && d.PartType == gptTypeBIOSBoot {
			out = append(out, d)
		}
	}
	return out, nil
}
