package sveltekitutils

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// This file implements a minimal, order-preserving JSON object model used by
// Adopt to edit package.json.
//
// Go's map[string]any has no defined iteration order, and json.Marshal on
// one alphabetizes every key at every nesting depth on the way back out. For
// a codemod meant to build trust in its first minutes of use, that turns a
// one-line real change (e.g. adding "pokkum:build" to "scripts") into a
// full-file reordering diff at every level the round-trip touches --
// including nested objects like "scripts" and "devDependencies", not just
// the top level. Decoding into this type instead and re-encoding it
// preserves the original member order end-to-end, at every depth, adding
// only genuinely new keys at the end of whichever object they were added to.

// orderedJSONValueKind identifies which case an orderedJSONValue holds.
type orderedJSONValueKind int

const (
	jsonKindObject orderedJSONValueKind = iota
	jsonKindArray
	jsonKindScalar
)

// orderedJSONObject is a JSON object that remembers member insertion order.
type orderedJSONObject struct {
	keys []string
	vals map[string]*orderedJSONValue
}

// orderedJSONValue is any JSON value: an order-preserving object, an array
// of values, or a scalar (string, json.Number, bool, or nil for JSON null).
type orderedJSONValue struct {
	kind   orderedJSONValueKind
	obj    *orderedJSONObject
	arr    []*orderedJSONValue
	scalar any
}

// decodeOrderedJSONObject parses data as a JSON document whose top-level
// value must be an object (true of any valid package.json), preserving
// member order at every nesting depth.
func decodeOrderedJSONObject(data []byte) (*orderedJSONObject, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber() // preserve exact numeric literals rather than lossy float64 round-tripping
	val, err := decodeOrderedJSONValue(dec)
	if err != nil {
		return nil, err
	}
	if val.kind != jsonKindObject {
		return nil, fmt.Errorf("top-level JSON value is not an object")
	}
	return val.obj, nil
}

func decodeOrderedJSONValue(dec *json.Decoder) (*orderedJSONValue, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}

	delim, isDelim := tok.(json.Delim)
	if !isDelim {
		// A scalar: string, json.Number, bool, or nil (JSON null).
		return &orderedJSONValue{kind: jsonKindScalar, scalar: tok}, nil
	}

	switch delim {
	case '{':
		obj := &orderedJSONObject{vals: make(map[string]*orderedJSONValue)}
		for dec.More() {
			keyTok, err := dec.Token()
			if err != nil {
				return nil, err
			}
			key, ok := keyTok.(string)
			if !ok {
				return nil, fmt.Errorf("expected object key, got %v", keyTok)
			}
			val, err := decodeOrderedJSONValue(dec)
			if err != nil {
				return nil, err
			}
			if _, exists := obj.vals[key]; !exists {
				obj.keys = append(obj.keys, key)
			}
			obj.vals[key] = val
		}
		if _, err := dec.Token(); err != nil { // consume closing '}'
			return nil, err
		}
		return &orderedJSONValue{kind: jsonKindObject, obj: obj}, nil
	case '[':
		var arr []*orderedJSONValue
		for dec.More() {
			v, err := decodeOrderedJSONValue(dec)
			if err != nil {
				return nil, err
			}
			arr = append(arr, v)
		}
		if _, err := dec.Token(); err != nil { // consume closing ']'
			return nil, err
		}
		return &orderedJSONValue{kind: jsonKindArray, arr: arr}, nil
	default:
		return nil, fmt.Errorf("unexpected JSON delimiter %q", delim)
	}
}

// has reports whether key is present.
func (o *orderedJSONObject) has(key string) bool {
	_, ok := o.vals[key]
	return ok
}

// getObject returns the object-typed value bound to key, if key is present
// and its value is a JSON object.
func (o *orderedJSONObject) getObject(key string) (*orderedJSONObject, bool) {
	v, ok := o.vals[key]
	if !ok || v.kind != jsonKindObject {
		return nil, false
	}
	return v.obj, true
}

// ensureObject returns the existing object bound to key, or creates and
// inserts a new empty one (appended after the object's current last member)
// if key is absent or bound to a non-object value.
func (o *orderedJSONObject) ensureObject(key string) *orderedJSONObject {
	if existing, ok := o.getObject(key); ok {
		return existing
	}
	obj := &orderedJSONObject{vals: make(map[string]*orderedJSONValue)}
	o.set(key, &orderedJSONValue{kind: jsonKindObject, obj: obj})
	return obj
}

// set inserts or updates key. An existing key keeps its original position;
// a new key is appended at the end, matching where a human editing the file
// by hand would naturally add it.
func (o *orderedJSONObject) set(key string, val *orderedJSONValue) {
	if _, exists := o.vals[key]; !exists {
		o.keys = append(o.keys, key)
	}
	o.vals[key] = val
}

// setString is a convenience wrapper around set for the common case of
// package.json leaf values (dependency version ranges, script commands).
func (o *orderedJSONObject) setString(key, s string) {
	o.set(key, &orderedJSONValue{kind: jsonKindScalar, scalar: s})
}

// delete removes key, if present, from both the value map and the order.
func (o *orderedJSONObject) delete(key string) {
	if _, exists := o.vals[key]; !exists {
		return
	}
	delete(o.vals, key)
	for i, k := range o.keys {
		if k == key {
			o.keys = append(o.keys[:i], o.keys[i+1:]...)
			break
		}
	}
}

// marshalIndent renders the document as indented JSON (2-space, matching
// the convention every real package.json on disk already uses), preserving
// member order at every depth. HTML-unsafe characters (<, >, &) are left
// unescaped -- encoding/json's default escaping would otherwise turn a
// perfectly ordinary, untouched script value like "vite build && vite-node"
// into literal && on every re-save, which is exactly the kind of
// incidental diff noise this whole type exists to eliminate.
func (o *orderedJSONObject) marshalIndent() ([]byte, error) {
	var buf bytes.Buffer
	if err := o.writeIndent(&buf, ""); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (o *orderedJSONObject) writeIndent(buf *bytes.Buffer, indent string) error {
	if len(o.keys) == 0 {
		buf.WriteString("{}")
		return nil
	}
	buf.WriteString("{\n")
	childIndent := indent + "  "
	for i, key := range o.keys {
		buf.WriteString(childIndent)
		keyJSON, err := marshalJSONNoHTMLEscape(key)
		if err != nil {
			return fmt.Errorf("marshal key %q: %w", key, err)
		}
		buf.Write(keyJSON)
		buf.WriteString(": ")
		if err := writeOrderedJSONValueIndent(buf, o.vals[key], childIndent); err != nil {
			return err
		}
		if i < len(o.keys)-1 {
			buf.WriteByte(',')
		}
		buf.WriteByte('\n')
	}
	buf.WriteString(indent + "}")
	return nil
}

func writeOrderedJSONValueIndent(buf *bytes.Buffer, v *orderedJSONValue, indent string) error {
	switch v.kind {
	case jsonKindObject:
		return v.obj.writeIndent(buf, indent)
	case jsonKindArray:
		return writeOrderedJSONArrayIndent(buf, v.arr, indent)
	default:
		b, err := marshalJSONNoHTMLEscape(v.scalar)
		if err != nil {
			return fmt.Errorf("marshal scalar value: %w", err)
		}
		buf.Write(b)
		return nil
	}
}

func writeOrderedJSONArrayIndent(buf *bytes.Buffer, arr []*orderedJSONValue, indent string) error {
	if len(arr) == 0 {
		buf.WriteString("[]")
		return nil
	}
	buf.WriteString("[\n")
	childIndent := indent + "  "
	for i, v := range arr {
		buf.WriteString(childIndent)
		if err := writeOrderedJSONValueIndent(buf, v, childIndent); err != nil {
			return err
		}
		if i < len(arr)-1 {
			buf.WriteByte(',')
		}
		buf.WriteByte('\n')
	}
	buf.WriteString(indent + "]")
	return nil
}

// marshalJSONNoHTMLEscape is json.Marshal without the default HTML-safe
// escaping of <, >, and & -- see marshalIndent's doc comment for why that
// default is wrong for re-serializing package.json content.
func marshalJSONNoHTMLEscape(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}
