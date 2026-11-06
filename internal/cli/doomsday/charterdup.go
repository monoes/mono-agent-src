package doomsday

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// checkDuplicateKeys refuses a JSON document in which an object repeats a key: encoding/json keeps the last value
// silently, so what a person read and what the CLI used could differ.
func checkDuplicateKeys(raw []byte) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	return dupValue(dec)
}

func dupValue(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return err
	}
	d, ok := tok.(json.Delim)
	if !ok {
		return nil
	}
	switch d {
	case '{':
		seen := map[string]bool{}
		for dec.More() {
			k, err := dec.Token()
			if err != nil {
				return err
			}
			key, _ := k.(string)
			if seen[key] {
				return fmt.Errorf("the charter names %q twice", key)
			}
			seen[key] = true
			if err := dupValue(dec); err != nil {
				return err
			}
		}
	case '[':
		for dec.More() {
			if err := dupValue(dec); err != nil {
				return err
			}
		}
	}
	_, err = dec.Token() // the closing delimiter
	return err
}
