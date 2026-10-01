package timings

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
)

// Get returns the seconds recorded for key.
func (o OrderedSeconds) Get(key string) (float64, bool) {
	for _, s := range o {
		if s.Key == key {
			return s.Seconds, true
		}
	}
	return 0, false
}

// MarshalJSON writes a JSON object in slice order.
func (o OrderedSeconds) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, s := range o {
		if i > 0 {
			b.WriteByte(',')
		}
		k, err := json.Marshal(s.Key)
		if err != nil {
			return nil, err
		}
		b.Write(k)
		b.WriteByte(':')
		b.WriteString(strconv.FormatFloat(s.Seconds, 'f', -1, 64))
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// UnmarshalJSON reads a JSON object of numbers, keeping key order.
func (o *OrderedSeconds) UnmarshalJSON(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return fmt.Errorf("steps: want a JSON object")
	}
	var out OrderedSeconds
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return err
		}
		key, _ := kt.(string)
		var v float64
		if err := dec.Decode(&v); err != nil {
			return fmt.Errorf("steps.%s: %w", key, err)
		}
		out = append(out, StepSeconds{Key: key, Seconds: v})
	}
	if _, err := dec.Token(); err != nil {
		return err
	}
	*o = out
	return nil
}
