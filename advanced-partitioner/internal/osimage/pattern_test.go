package osimage

import "testing"

func TestParsePattern(t *testing.T) {
	cases := map[string]string{
		"etc/hosts":          "etc/hosts",
		"etc/ssh/ssh_host_*": "etc/ssh/ssh_host_*", // no trailing slash → base == raw
		"var/log/***":        "var/log",
		"var/log/**":         "var/log",
		"var/log/*":          "var/log",
		"home/":              "home",
		"":                   "",
		"a":                  "a",
		"a/b/c/***":          "a/b/c",
	}
	for raw, wantBase := range cases {
		p := ParsePattern(raw)
		if p.Raw != raw {
			t.Fatalf("Raw = %q, want %q", p.Raw, raw)
		}
		if p.Base != wantBase {
			t.Fatalf("ParsePattern(%q).Base = %q, want %q", raw, p.Base, wantBase)
		}
	}
}

func TestMatchExactAndChildren(t *testing.T) {
	// Bash $pbase/* case always fires for any child, so even an
	// "exact-looking" pattern matches its subtree if one exists.
	p := ParsePattern("etc/hosts")
	for _, ok := range []string{
		"etc/hosts",
		"etc/hosts/anything",
	} {
		if !p.Match(ok) {
			t.Fatalf("exact pattern should match %q", ok)
		}
	}
	for _, bad := range []string{"etc/hostsX", "etc/host", ""} {
		if p.Match(bad) {
			t.Fatalf("exact should NOT match %q", bad)
		}
	}
}

func TestMatchTrailingGlob(t *testing.T) {
	p := ParsePattern("etc/ssh/ssh_host_*")
	ok := []string{
		"etc/ssh/ssh_host_rsa",
		"etc/ssh/ssh_host_dsa",
		"etc/ssh/ssh_host_ecdsa_key",
	}
	for _, x := range ok {
		if !p.Match(x) {
			t.Fatalf("trailing glob should match %q", x)
		}
	}
	for _, bad := range []string{"etc/ssh/passwd", "etc/sshd"} {
		if p.Match(bad) {
			t.Fatalf("trailing glob should NOT match %q", bad)
		}
	}
}

func TestMatchSubtree(t *testing.T) {
	p := ParsePattern("var/log/***")
	ok := []string{
		"var/log",          // exact (matches Base)
		"var/log/messages", // direct child
		"var/log/dnf/dnf.log",
		"var/log/audit/audit.log",
	}
	for _, x := range ok {
		if !p.Match(x) {
			t.Fatalf("subtree should match %q", x)
		}
	}
	for _, bad := range []string{"var/loglevel", "varX/log"} {
		if p.Match(bad) {
			t.Fatalf("subtree should NOT match %q", bad)
		}
	}
}

func TestMatchSubtreeViaRawGlob(t *testing.T) {
	// Pattern "var/log/*" (single star) — bash treats this the same as
	// "var/log/**" because case-`*` matches '/'. Our globMatch follows
	// the same rule.
	p := ParsePattern("var/log/*")
	for _, ok := range []string{
		"var/log/foo",
		"var/log/foo/bar",
		"var/log/deep/sub/path",
	} {
		if !p.Match(ok) {
			t.Fatalf("subtree glob should match %q", ok)
		}
	}
}

func TestGlobMatchStarMatchesSlash(t *testing.T) {
	// The crucial divergence from filepath.Match: * spans '/'.
	cases := map[[2]string]bool{
		{"var/*", "var/log"}:           true,
		{"var/*", "var/log/foo"}:       true,
		{"var/*", "varlog"}:            false,
		{"*", "anything"}:              true,
		{"a/?/c", "a/b/c"}:             true,
		{"a/?/c", "a/bb/c"}:            false,
		{"*.tar.bz2", "image.tar.bz2"}: true,
		{"*.tar.bz2", "image.tar.gz"}:  false,
	}
	for k, want := range cases {
		got := globMatch(k[0], k[1])
		if got != want {
			t.Fatalf("globMatch(%q, %q) = %v, want %v", k[0], k[1], got, want)
		}
	}
}

func TestGlobMatchCollapsesStars(t *testing.T) {
	// `*`, `**`, `***` all behave identically.
	for _, p := range []string{"*", "**", "***"} {
		if !globMatch(p, "anything") {
			t.Fatalf("%q should match 'anything'", p)
		}
	}
}
