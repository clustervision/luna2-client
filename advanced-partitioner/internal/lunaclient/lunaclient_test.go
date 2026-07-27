package lunaclient

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
)

func validParams() PinParams {
	return PinParams{
		BaseURL:    "https://controller:7050",
		Node:       "node001",
		Token:      "tok-abc",
		Disklayout: []byte(`{"version":2,"sets":[]}`),
	}
}

func TestEndpoint(t *testing.T) {
	p := validParams()
	if got := p.Endpoint(); got != "https://controller:7050/config/node/node001" {
		t.Fatalf("endpoint: %q", got)
	}
	p.BaseURL = "https://controller:7050/" // trailing slash trimmed
	if got := p.Endpoint(); got != "https://controller:7050/config/node/node001" {
		t.Fatalf("endpoint with trailing slash: %q", got)
	}
}

func TestBodyBytesShape(t *testing.T) {
	p := validParams()
	p.Disklayout = []byte(`{"version":2,"sets":[{"name":"os"}]}`)
	raw, err := p.BodyBytes()
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Config struct {
			Node map[string]struct {
				Disklayout string `json:"disklayout"`
			} `json:"node"`
		} `json:"config"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("body is not the config.node.<name>.disklayout shape: %v", err)
	}
	got, ok := decoded.Config.Node["node001"]
	if !ok {
		t.Fatalf("node001 missing from body: %s", raw)
	}
	// disklayout must be base64-encoded (the daemon mangles raw JSON; base64
	// round-trips and the node base64-decodes it). Decode and compare.
	dec, err := base64.StdEncoding.DecodeString(got.Disklayout)
	if err != nil {
		t.Fatalf("disklayout is not base64: %v (got %q)", err, got.Disklayout)
	}
	if string(dec) != string(p.Disklayout) {
		t.Errorf("decoded disklayout mismatch:\n got %q\nwant %q", dec, p.Disklayout)
	}
}

func TestValidate(t *testing.T) {
	cases := map[string]func(*PinParams){
		"missing url":   func(p *PinParams) { p.BaseURL = "" },
		"missing node":  func(p *PinParams) { p.Node = "" },
		"missing token": func(p *PinParams) { p.Token = "" },
		"empty layout":  func(p *PinParams) { p.Disklayout = nil },
	}
	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			p := validParams()
			mut(&p)
			if err := p.Validate(); err == nil {
				t.Fatalf("%s: expected validation error", name)
			}
		})
	}
	if err := validParams().Validate(); err != nil {
		t.Fatalf("valid params rejected: %v", err)
	}
}

func TestCurlArgs(t *testing.T) {
	p := validParams()
	p.Insecure = true
	args := p.curlArgs()
	joined := strings.Join(args, " ")

	// POST to the right endpoint, body on stdin, status-code probe, insecure.
	wantContains := []string{
		"--insecure", "-X POST", "--data-binary @-",
		"x-access-tokens: tok-abc", "Content-Type: application/json",
		"%{http_code}", p.Endpoint(),
	}
	for _, w := range wantContains {
		if !strings.Contains(joined, w) {
			t.Errorf("curlArgs missing %q\n  got: %s", w, joined)
		}
	}

	// Without Insecure, --insecure must NOT be present.
	p.Insecure = false
	if strings.Contains(strings.Join(p.curlArgs(), " "), "--insecure") {
		t.Errorf("--insecure must be omitted when Insecure=false")
	}
}

// Integration: drive the real curl against an httptest server. Skipped where
// curl is unavailable (matching lpartinputs, the curl call is otherwise on-node).
func TestPinDisklayoutViaCurl(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl not available")
	}

	t.Run("success", func(t *testing.T) {
		var gotToken, gotBody, gotPath string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotToken = r.Header.Get("x-access-tokens")
			gotPath = r.URL.Path
			b, _ := io.ReadAll(r.Body)
			gotBody = string(b)
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()

		p := validParams()
		p.BaseURL = srv.URL
		if err := PinDisklayout(context.Background(), p); err != nil {
			t.Fatalf("PinDisklayout: %v", err)
		}
		if gotToken != "tok-abc" {
			t.Errorf("server saw token %q", gotToken)
		}
		if gotPath != "/config/node/node001" {
			t.Errorf("server saw path %q", gotPath)
		}
		if !strings.Contains(gotBody, `"disklayout"`) {
			t.Errorf("server body missing disklayout: %s", gotBody)
		}
	})

	t.Run("non-2xx is an error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		}))
		defer srv.Close()

		p := validParams()
		p.BaseURL = srv.URL
		err := PinDisklayout(context.Background(), p)
		if err == nil {
			t.Fatal("expected an error on HTTP 401")
		}
		if !strings.Contains(err.Error(), "401") {
			t.Errorf("error should carry the status: %v", err)
		}
	})
}
