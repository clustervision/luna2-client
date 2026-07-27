package osimage

import "testing"

// ProvisionOrder must honour the daemon-resolved method/fallback rather than
// freezing torrent-first. Empty => the historical torrent-then-http default.
func TestProvisionOrder(t *testing.T) {
	cases := []struct {
		name, method, fallback string
		want                   []string
	}{
		{"empty defaults to torrent-then-http", "", "", []string{"torrent", "http"}},
		{"http-only skips torrent", "http", "", []string{"http"}},
		{"http with torrent fallback", "http", "torrent", []string{"http", "torrent"}},
		{"torrent with http fallback", "torrent", "http", []string{"torrent", "http"}},
		{"dedup identical", "http", "http", []string{"http"}},
		{"case/space normalised", " Torrent ", "HTTP", []string{"torrent", "http"}},
		{"fallback only", "", "http", []string{"http"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ProvisionOrder(c.method, c.fallback)
			if len(got) != len(c.want) {
				t.Fatalf("ProvisionOrder(%q,%q) = %v, want %v", c.method, c.fallback, got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Fatalf("ProvisionOrder(%q,%q) = %v, want %v", c.method, c.fallback, got, c.want)
				}
			}
		})
	}
}

// FileBaseFromLunaURL must derive the file server from the cmdline luna.url
// host (NOT a hardcoded controller IP) — http://<host>:7051/files.
func TestFileBaseFromLunaURL(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://10.141.255.252:7050", "http://10.141.255.252:7051/files"},
		{"http://controller1:7050/", "http://controller1:7051/files"},
		{"https://10.141.255.254:7050/some/path", "http://10.141.255.254:7051/files"},
		{"https://controller.example.com:7050", "http://controller.example.com:7051/files"},
		{"", ""},
		{"   ", ""},
	}
	for _, c := range cases {
		if got := FileBaseFromLunaURL(c.in); got != c.want {
			t.Fatalf("FileBaseFromLunaURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
