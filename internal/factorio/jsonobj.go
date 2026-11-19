package factorio

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

// errNotObject reports JSON content that is not a single JSON object.
var errNotObject = errors.New("not a JSON object")

// member is one key/value pair of a JSON object.
type member struct {
	Key   string
	Value json.RawMessage
}

// object is a JSON object that keeps the order of its keys, so files such
// as server-settings.json keep their layout (and their "_comment_" keys
// next to the keys they describe) when the manager changes one value.
type object []member

// parseObject parses data, which must be exactly one JSON object.
func parseObject(data []byte) (object, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, errNotObject
	}
	obj := object{}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := tok.(string)
		if !ok {
			return nil, errNotObject
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, err
		}
		obj = obj.set(key, raw)
	}
	if _, err := dec.Token(); err != nil { // the closing '}'
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("unexpected data after the JSON object")
	}
	return obj, nil
}

// get returns the raw value of key.
func (o object) get(key string) (json.RawMessage, bool) {
	for _, m := range o {
		if m.Key == key {
			return m.Value, true
		}
	}
	return nil, false
}

// set replaces the value of key in place, or appends key at the end.
func (o object) set(key string, value json.RawMessage) object {
	for i := range o {
		if o[i].Key == key {
			o[i].Value = value
			return o
		}
	}
	return append(o, member{Key: key, Value: value})
}

// setValue is set with value encoded as JSON (without HTML escaping).
func (o object) setValue(key string, value any) (object, error) {
	raw, err := encodeJSON(value)
	if err != nil {
		return o, err
	}
	return o.set(key, raw), nil
}

// subObject returns the object stored under key; a missing key or a value
// that is not an object gives an empty object.
func (o object) subObject(key string) object {
	raw, ok := o.get(key)
	if !ok {
		return object{}
	}
	sub, err := parseObject(raw)
	if err != nil {
		return object{}
	}
	return sub
}

// compact returns o as compact JSON.
func (o object) compact() json.RawMessage {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, m := range o {
		if i > 0 {
			b.WriteByte(',')
		}
		key, _ := encodeJSON(m.Key) // encoding a string cannot fail
		b.Write(key)
		b.WriteByte(':')
		b.Write(m.Value)
	}
	b.WriteByte('}')
	return b.Bytes()
}

// indented returns o as JSON indented with two spaces and a final newline,
// the format of every config file the manager writes.
func (o object) indented() ([]byte, error) {
	var out bytes.Buffer
	if err := json.Indent(&out, o.compact(), "", "  "); err != nil {
		return nil, err
	}
	out.WriteByte('\n')
	return out.Bytes(), nil
}

// encodeJSON encodes v without HTML escaping and without the trailing
// newline json.Encoder adds.
func encodeJSON(v any) (json.RawMessage, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(b.Bytes(), "\n"), nil
}

// decodeJSON decodes raw into a Go value with numbers as json.Number.
func decodeJSON(raw json.RawMessage) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return v, nil
}
