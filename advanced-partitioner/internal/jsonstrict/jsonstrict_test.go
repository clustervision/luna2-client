package jsonstrict

import (
	"strings"
	"testing"
)

func TestCheck(t *testing.T) {
	cases := []struct {
		name, doc, want string
	}{
		{"clean", `{"version":2,"sets":[{"name":"os","devices":["/dev/vda"]}]}`, ""},
		{"dup-top", `{"version":2,"version":2}`, `top-level: key "version" appears twice`},
		{"dup-nested", `{"sets":[{"name":"a"},{"devices":["/dev/a","/dev/b"],"devices":["/dev/c"]}]}`, `sets[1]: key "devices" appears twice`},
		{"case-fold", `{"sets":[{"name":"os","NAME":"x"}]}`, `sets[0]: keys "name" and "NAME" are the same field`},
		{"trailing", `{"version":2} {"version":3}`, "unexpected data after the JSON document"},
		{"not-json", `{"version":`, "unexpected EOF"},
	}
	for _, c := range cases {
		err := Check([]byte(c.doc))
		if c.want == "" {
			if err != nil {
				t.Errorf("%s: unexpected error %v", c.name, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v, want %q", c.name, err, c.want)
		}
	}
}
