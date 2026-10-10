// Package jsonstrict holds the checks encoding/json does not make for the
// operator documents lpart reads (disklayout, osimage_filter): duplicate object
// keys and trailing data. encoding/json merges duplicate keys into one struct
// (and matches keys case-insensitively), so an earlier occurrence can set a
// field that a later one hides from every key check, and stale slice elements
// can survive ("devices": ["a","b"] then "devices": ["c"] decodes to [c b]).
package jsonstrict

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Check returns an error when raw is not exactly one JSON value, or when an
// object in it repeats a key. Keys are compared case-insensitively, the way
// encoding/json matches them to struct fields. The error names the object by
// its path (top-level, sets[1], sets[1].volumes[0], ...).
func Check(raw []byte) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := walk(dec, "top-level"); err != nil {
		return err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return fmt.Errorf("unexpected data after the JSON document")
	}
	return nil
}

func walk(dec *json.Decoder, path string) error {
	tok, err := token(dec)
	if err != nil {
		return err
	}
	d, ok := tok.(json.Delim)
	if !ok {
		return nil // scalar
	}
	switch d {
	case '{':
		seen := map[string]string{}
		for dec.More() {
			kt, err := token(dec)
			if err != nil {
				return err
			}
			key, _ := kt.(string)
			fold := strings.ToLower(key)
			if prev, dup := seen[fold]; dup {
				if prev == key {
					return fmt.Errorf("%s: key %q appears twice", path, key)
				}
				return fmt.Errorf("%s: keys %q and %q are the same field (JSON keys match case-insensitively)", path, prev, key)
			}
			seen[fold] = key
			if err := walk(dec, child(path, key)); err != nil {
				return err
			}
		}
	case '[':
		for i := 0; dec.More(); i++ {
			if err := walk(dec, fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
	}
	_, err = token(dec) // closing delimiter
	return err
}

// token is dec.Token with a truncated document reported as such.
func token(dec *json.Decoder) (json.Token, error) {
	t, err := dec.Token()
	if errors.Is(err, io.EOF) {
		return nil, io.ErrUnexpectedEOF
	}
	return t, err
}

func child(path, key string) string {
	if path == "top-level" {
		return key
	}
	return path + "." + key
}
