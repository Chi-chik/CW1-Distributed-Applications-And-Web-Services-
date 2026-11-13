package ipc

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

// strictUnmarshal is json.Unmarshal with unknown fields rejected and
// trailing data refused.
func strictUnmarshal(b []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("trailing data after JSON value")
	}
	return nil
}
