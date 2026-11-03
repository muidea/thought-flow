package documentset

import (
	"bytes"
	"encoding/json"
	"io"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

const PublicJSONVersion = "public-json-jcs-v1"

// CanonicalPublicJSON implements the JCS subset fixed by the public schema:
// only safe integers are legal numbers. It rejects duplicate keys BEFORE decode.
func CanonicalPublicJSON(raw []byte) ([]byte, error) {
	if !utf8.Valid(raw) || !validJSONSurrogates(raw) {
		return nil, ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var read func(int) (any, error)
	read = func(depth int) (any, error) {
		if depth > 64 {
			return nil, ErrInvalid
		}
		t, e := d.Token()
		if e != nil {
			return nil, e
		}
		switch v := t.(type) {
		case json.Delim:
			if v == '{' {
				m := map[string]any{}
				for d.More() {
					k, e := d.Token()
					if e != nil {
						return nil, e
					}
					s, ok := k.(string)
					if !ok {
						return nil, ErrInvalid
					}
					if _, ok = m[s]; ok {
						return nil, ErrInvalid
					}
					x, e := read(depth + 1)
					if e != nil {
						return nil, e
					}
					m[s] = x
				}
				_, e = d.Token()
				return m, e
			}
			if v == '[' {
				a := []any{}
				for d.More() {
					x, e := read(depth + 1)
					if e != nil {
						return nil, e
					}
					a = append(a, x)
				}
				_, e = d.Token()
				return a, e
			}
			return nil, ErrInvalid
		case json.Number:
			if len(v) > 128 {
				return nil, ErrInvalid
			}
			if i := strings.IndexAny(string(v), "eE"); i >= 0 {
				exp, e := strconv.Atoi(string(v)[i+1:])
				if e != nil || exp > 308 || exp < -308 {
					return nil, ErrInvalid
				}
			}
			n, ok := new(big.Rat).SetString(string(v))
			if !ok || !n.IsInt() {
				return nil, ErrInvalid
			}
			i := n.Num()
			if !i.IsInt64() || i.Int64() > 9007199254740991 || i.Int64() < -9007199254740991 {
				return nil, ErrInvalid
			}
			return json.Number(i.String()), nil
		default:
			return t, nil
		}
	}
	value, e := read(0)
	if e != nil {
		return nil, e
	}
	if _, e = d.Token(); e != io.EOF {
		return nil, ErrInvalid
	}
	var out bytes.Buffer
	quote := func(s string) {
		out.WriteByte('"')
		for _, r := range s {
			switch r {
			case '"', '\\':
				out.WriteByte('\\')
				out.WriteRune(r)
			case '\b':
				out.WriteString(`\b`)
			case '\f':
				out.WriteString(`\f`)
			case '\n':
				out.WriteString(`\n`)
			case '\r':
				out.WriteString(`\r`)
			case '\t':
				out.WriteString(`\t`)
			default:
				if r < 0x20 {
					const digits = "0123456789abcdef"
					out.WriteString(`\u00`)
					out.WriteByte(digits[byte(r)>>4])
					out.WriteByte(digits[byte(r)&15])
				} else {
					out.WriteRune(r)
				}
			}
		}
		out.WriteByte('"')
	}
	var write func(any)
	write = func(v any) {
		switch x := v.(type) {
		case nil:
			out.WriteString("null")
		case bool:
			out.WriteString(strconv.FormatBool(x))
		case json.Number:
			out.WriteString(string(x))
		case string:
			quote(x)
		case []any:
			out.WriteByte('[')
			for i, v := range x {
				if i > 0 {
					out.WriteByte(',')
				}
				write(v)
			}
			out.WriteByte(']')
		case map[string]any:
			keys := []string{}
			for k := range x {
				keys = append(keys, k)
			}
			sort.Slice(keys, func(i, j int) bool {
				a, b := utf16.Encode([]rune(keys[i])), utf16.Encode([]rune(keys[j]))
				for n := 0; n < len(a) && n < len(b); n++ {
					if a[n] != b[n] {
						return a[n] < b[n]
					}
				}
				return len(a) < len(b)
			})
			out.WriteByte('{')
			for i, k := range keys {
				if i > 0 {
					out.WriteByte(',')
				}
				quote(k)
				out.WriteByte(':')
				write(x[k])
			}
			out.WriteByte('}')
		}
	}
	write(value)
	return out.Bytes(), nil
}
func PublicJSON(v any) ([]byte, error) {
	raw, e := json.Marshal(v)
	if e != nil {
		return nil, e
	}
	return CanonicalPublicJSON(raw)
}

func validJSONSurrogates(raw []byte) bool {
	for i := 0; i < len(raw); i++ {
		if raw[i] != '\\' {
			continue
		}
		if i+1 >= len(raw) {
			return false
		}
		if raw[i+1] != 'u' {
			i++
			continue
		}
		if i+6 > len(raw) {
			return false
		}
		n, e := strconv.ParseUint(string(raw[i+2:i+6]), 16, 16)
		if e != nil {
			return false
		}
		if n >= 0xd800 && n <= 0xdbff {
			if i+12 > len(raw) || raw[i+6] != '\\' || raw[i+7] != 'u' {
				return false
			}
			low, e := strconv.ParseUint(string(raw[i+8:i+12]), 16, 16)
			if e != nil || low < 0xdc00 || low > 0xdfff {
				return false
			}
			i += 11
		} else {
			if n >= 0xdc00 && n <= 0xdfff {
				return false
			}
			i += 5
		}
	}
	return true
}
