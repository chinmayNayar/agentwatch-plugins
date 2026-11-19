package install

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// Obj is a JSON object that keeps its key order, so merging our hooks into a
// user's settings file does not reshuffle the rest of it.
type Obj struct {
	Keys []string
	Vals map[string]any // values: *Obj, []any, string, json.Number, bool, nil
}

func NewObj() *Obj { return &Obj{Vals: map[string]any{}} }

func (o *Obj) Get(k string) (any, bool) {
	v, ok := o.Vals[k]
	return v, ok
}

func (o *Obj) Set(k string, v any) {
	if _, ok := o.Vals[k]; !ok {
		o.Keys = append(o.Keys, k)
	}
	o.Vals[k] = v
}

func (o *Obj) Delete(k string) {
	if _, ok := o.Vals[k]; !ok {
		return
	}
	delete(o.Vals, k)
	for i, x := range o.Keys {
		if x == k {
			o.Keys = append(o.Keys[:i], o.Keys[i+1:]...)
			break
		}
	}
}

// ObjAt returns o[k] as an object, creating it when absent.
func (o *Obj) ObjAt(k string) (*Obj, error) {
	v, ok := o.Vals[k]
	if !ok || v == nil {
		n := NewObj()
		o.Set(k, n)
		return n, nil
	}
	if x, ok := v.(*Obj); ok {
		return x, nil
	}
	return nil, fmt.Errorf("%q is not an object", k)
}

// Parse decodes JSON keeping key order. Empty input is an empty object.
func Parse(b []byte) (*Obj, error) {
	if len(bytes.TrimSpace(b)) == 0 {
		return NewObj(), nil
	}
	dec := json.NewDecoder(bytes.NewReader(stripBOM(b)))
	dec.UseNumber()
	v, err := parseValue(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("trailing data after JSON")
	}
	o, ok := v.(*Obj)
	if !ok {
		return nil, fmt.Errorf("top level is not an object")
	}
	return o, nil
}

func stripBOM(b []byte) []byte { return bytes.TrimPrefix(b, []byte("\xef\xbb\xbf")) }

func parseValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			o := NewObj()
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return nil, err
				}
				k, ok := kt.(string)
				if !ok {
					return nil, fmt.Errorf("object key is not a string")
				}
				v, err := parseValue(dec)
				if err != nil {
					return nil, err
				}
				o.Set(k, v)
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return o, nil
		case '[':
			arr := []any{}
			for dec.More() {
				v, err := parseValue(dec)
				if err != nil {
					return nil, err
				}
				arr = append(arr, v)
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return arr, nil
		}
		return nil, fmt.Errorf("unexpected %v", t)
	default:
		return t, nil
	}
}

// Marshal renders with two-space indentation and a trailing newline.
func (o *Obj) Marshal() []byte {
	var b strings.Builder
	writeValue(&b, o, 0)
	b.WriteByte('\n')
	return []byte(b.String())
}

func writeValue(b *strings.Builder, v any, ind int) {
	pad := strings.Repeat("  ", ind)
	switch t := v.(type) {
	case *Obj:
		if len(t.Keys) == 0 {
			b.WriteString("{}")
			return
		}
		b.WriteString("{\n")
		for i, k := range t.Keys {
			b.WriteString(pad + "  ")
			b.Write(enc(k))
			b.WriteString(": ")
			writeValue(b, t.Vals[k], ind+1)
			if i < len(t.Keys)-1 {
				b.WriteByte(',')
			}
			b.WriteByte('\n')
		}
		b.WriteString(pad + "}")
	case []any:
		if len(t) == 0 {
			b.WriteString("[]")
			return
		}
		b.WriteString("[\n")
		for i, x := range t {
			b.WriteString(pad + "  ")
			writeValue(b, x, ind+1)
			if i < len(t)-1 {
				b.WriteByte(',')
			}
			b.WriteByte('\n')
		}
		b.WriteString(pad + "]")
	default:
		b.Write(enc(t))
	}
}

func enc(v any) []byte {
	var buf bytes.Buffer
	e := json.NewEncoder(&buf)
	e.SetEscapeHTML(false)
	_ = e.Encode(v)
	return bytes.TrimRight(buf.Bytes(), "\n")
}

// FromMap converts plain Go values (map[string]any with sorted keys) to Obj.
func FromMap(m map[string]any, order ...string) *Obj {
	o := NewObj()
	seen := map[string]bool{}
	for _, k := range order {
		if v, ok := m[k]; ok {
			o.Set(k, conv(v))
			seen[k] = true
		}
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		if !seen[k] {
			keys = append(keys, k)
		}
	}
	sortStrings(keys)
	for _, k := range keys {
		o.Set(k, conv(m[k]))
	}
	return o
}

func conv(v any) any {
	switch t := v.(type) {
	case map[string]any:
		return FromMap(t, "type", "matcher", "command", "args", "bash", "powershell", "timeout", "timeoutSec")
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = conv(x)
		}
		return out
	case []string:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = x
		}
		return out
	case int:
		return json.Number(fmt.Sprint(t))
	}
	return v
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
