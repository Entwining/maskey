// Package redact masks exact byte occurrences of protected values in a
// stream as it passes through.
package redact

import (
	"bytes"
	"errors"
	"io"
	"slices"
)

// Writer masks protected values in what is written to it before passing it
// on. Every byte covered by an occurrence of any value, overlapping
// occurrences included, belongs to a masked run, and each run becomes one
// marker. Writer keeps back only a trailing part of the stream that begins
// some value, so output such as a prompt flows as it arrives and memory
// stays bounded by the longest value. After a write fails it passes nothing
// on.
type Writer struct {
	w      io.Writer
	values []value
	marker []byte
	// pending holds bytes not yet decided: a trailing part of the stream
	// that is a proper prefix of some value.
	pending []byte
	// covered counts the leading pending bytes inside an occurrence that
	// began in bytes already passed on.
	covered int
	// masking reports whether the last output was a marker, so a run that
	// continues past a write boundary stays one marker.
	masking bool
	err     error
}

type value struct {
	bytes []byte
	// border[i] is the length of the longest proper prefix of bytes[:i+1]
	// that is also its suffix (the Knuth-Morris-Pratt failure function).
	border []int
}

// NewWriter returns a Writer that passes masked output to w. Values must be
// nonempty and free of NUL bytes.
func NewWriter(w io.Writer, values [][]byte) (*Writer, error) {
	var unique [][]byte
	for _, v := range values {
		if len(v) == 0 || bytes.IndexByte(v, 0) >= 0 {
			return nil, errors.New("a protected value is empty or contains a NUL byte")
		}
		if !slices.ContainsFunc(unique, func(u []byte) bool { return bytes.Equal(u, v) }) {
			unique = append(unique, v)
		}
	}
	r := &Writer{w: w, marker: Marker(unique)}
	for _, v := range unique {
		border := make([]int, len(v))
		for i, k := 1, 0; i < len(v); i++ {
			for k > 0 && v[i] != v[k] {
				k = border[k-1]
			}
			if v[i] == v[k] {
				k++
			}
			border[i] = k
		}
		r.values = append(r.values, value{bytes: v, border: border})
	}
	return r, nil
}

// Marker returns the replacement for masked runs. It repeats a byte that
// occurs in none of the values: the masked output then cannot contain a
// value, because an occurrence would either include a marker byte or lie
// entirely within unmasked bytes that were contiguous in the input, where
// it would have been masked. NUL is the last resort and always qualifies.
func Marker(values [][]byte) []byte {
	candidates := []byte("*#~%^")
	for b := byte('!'); b <= '~'; b++ {
		candidates = append(candidates, b)
	}
	for _, b := range append(candidates, 0) {
		if !slices.ContainsFunc(values, func(v []byte) bool { return bytes.IndexByte(v, b) >= 0 }) {
			return bytes.Repeat([]byte{b}, 3)
		}
	}
	panic("unreachable: values cannot contain NUL")
}

func (w *Writer) Write(p []byte) (int, error) {
	if err := w.process(p, false); err != nil {
		return 0, err
	}
	return len(p), nil
}

// Close passes on the bytes kept back at the end of the stream. It does not
// close the underlying writer.
func (w *Writer) Close() error { return w.process(nil, true) }

func (w *Writer) process(p []byte, final bool) error {
	if w.err != nil {
		return w.err
	}
	buf := p
	if len(w.pending) > 0 {
		buf = append(w.pending, p...)
	}
	// An occurrence starting before decided would otherwise leave a longer
	// value prefix at the end of buf, so every such occurrence lies within
	// buf and the coverage of the bytes before decided is complete.
	decided := len(buf)
	if !final {
		for _, v := range w.values {
			decided = min(decided, len(buf)-v.openPrefix(buf))
		}
	}

	// Keep ends nil when no bytes before decided need masking, so that
	// output can reuse buf without a copy.
	var ends []int
	if w.covered > 0 {
		ends = make([]int, decided)
	}
	for _, v := range w.values {
		for start := 0; start < decided; {
			i := bytes.Index(buf[start:], v.bytes)
			if i < 0 || start+i >= decided {
				break
			}
			if ends == nil {
				ends = make([]int, decided)
			}
			at := start + i
			ends[at] = max(ends[at], at+len(v.bytes))
			start = at + 1
		}
	}

	var out []byte
	coverEnd := w.covered
	if ends == nil {
		out = buf[:decided]
		if decided > 0 {
			w.masking = false
		}
	} else {
		for i := range decided {
			coverEnd = max(coverEnd, ends[i])
			if i < coverEnd {
				if !w.masking {
					out = append(out, w.marker...)
					w.masking = true
				}
				continue
			}
			out = append(out, buf[i])
			w.masking = false
		}
	}
	w.covered = max(coverEnd-decided, 0)
	w.pending = append([]byte(nil), buf[decided:]...)

	if len(out) > 0 {
		if _, err := w.w.Write(out); err != nil {
			w.err = err
			return err
		}
	}
	return nil
}

// openPrefix returns the length of the longest suffix of buf that is a
// proper prefix of v.
func (v value) openPrefix(buf []byte) int {
	// Scanning fewer bytes than the value keeps k below its length.
	k := 0
	for _, b := range buf[max(len(buf)-len(v.bytes)+1, 0):] {
		for k > 0 && v.bytes[k] != b {
			k = v.border[k-1]
		}
		if v.bytes[k] == b {
			k++
		}
	}
	return k
}
