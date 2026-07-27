package engine

import "testing"

func TestParseKmsg(t *testing.T) {
	cases := []struct {
		name      string
		line      string
		wantOK    bool
		wantMsg   string
		wantLevel int
	}{
		{"err record", "3,123,456,-;ipmi_si: could not find device", true, "ipmi_si: could not find device", 3},
		{"warn record", "4,1,2,-;ata1: link is slow to respond", true, "ata1: link is slow to respond", 4},
		{"facility+level err", "11,9,9,-;EXT4-fs error", true, "EXT4-fs error", 3},
		{"info kept", "6,1,2,-;usb 1-1: new high-speed USB device", true, "usb 1-1: new high-speed USB device", 6},
		{"debug dropped", "7,1,2,-;debug spew", false, "", 0},
		{"continuation line", " SUBSYSTEM=acpi", false, "", 0},
		{"no semicolon", "garbage line", false, "", 0},
		{"emerg kept", "0,1,2,-;kernel panic imminent", true, "kernel panic imminent", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ev, ok := parseKmsg(c.line)
			if ok != c.wantOK {
				t.Fatalf("ok=%v want %v", ok, c.wantOK)
			}
			if !ok {
				return
			}
			if ev.Type != EvKernel || ev.Stream != StreamKernel {
				t.Errorf("type/stream = %v/%v", ev.Type, ev.Stream)
			}
			if ev.Message != c.wantMsg {
				t.Errorf("msg = %q want %q", ev.Message, c.wantMsg)
			}
			if lvl, _ := ev.Data.(int); lvl != c.wantLevel {
				t.Errorf("level = %d want %d", lvl, c.wantLevel)
			}
		})
	}
}
