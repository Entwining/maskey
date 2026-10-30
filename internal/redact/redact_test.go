package redact

import (
	"bytes"
	"errors"
	"math/rand/v2"
	"testing"
)

func mask(t *testing.T, values []string, chunks ...[]byte) []byte {
	t.Helper()
	var out bytes.Buffer
	w, err := NewWriter(&out, toBytes(values))
	if err != nil {
		t.Fatal(err)
	}
	for _, chunk := range chunks {
		if _, err := w.Write(chunk); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func toBytes(values []string) [][]byte {
	b := make([][]byte, len(values))
	for i, v := range values {
		b[i] = []byte(v)
	}
	return b
}

// Each case is checked as one write, split at every position, and byte by
// byte, so occurrences crossing write boundaries and values left pending at
// the end of the stream are covered.
func TestMasking(t *testing.T) {
	for _, test := range []struct {
		name   string
		values []string
		input  string
		want   string
	}{
		{"token", []string{"tok-3f9a"}, "auth tok-3f9a ok", "auth *** ok"},
		{"prefix at end of stream", []string{"tok-3f9a"}, "ends with tok-3f", "ends with tok-3f"},
		{"overlapping occurrences", []string{"aba"}, "xababax", "x***x"},
		{"adjacent occurrences", []string{"ab"}, "abab", "***"},
		{"value inside another", []string{"secret", "cre"}, "a secret, cre", "a ***, ***"},
		{"equal values", []string{"dup", "dup"}, "dup dup", "*** ***"},
		{"one byte", []string{"e"}, "the end", "th*** ***nd"},
		{"multiline value", []string{"line1\nline2"}, "<line1\nline2>\n", "<***>\n"},
		{"marker byte in value", []string{"*"}, "a*b", "a###b"},
		{"non-UTF-8", []string{"\xc3\x28"}, "\xff\xfe\xc3\x28\x80", "\xff\xfe***\x80"},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := []byte(test.input)
			if got := mask(t, test.values, input); string(got) != test.want {
				t.Fatalf("one write: %q, want %q", got, test.want)
			}
			for i := 0; i <= len(input); i++ {
				if got := mask(t, test.values, input[:i], input[i:]); string(got) != test.want {
					t.Fatalf("split at %d: %q, want %q", i, got, test.want)
				}
			}
			chunks := make([][]byte, len(input))
			for i := range input {
				chunks[i] = input[i : i+1]
			}
			if got := mask(t, test.values, chunks...); string(got) != test.want {
				t.Fatalf("byte by byte: %q, want %q", got, test.want)
			}
		})
	}
}

// Random streams over a small alphabet produce many overlapping and
// partial occurrences. The output must equal masking computed over the
// whole input at once, and must never contain a value.
func TestRandomStreams(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	alphabet := []byte("ab*\n")
	random := func(n int) []byte {
		b := make([]byte, n)
		for i := range b {
			b[i] = alphabet[r.IntN(len(alphabet))]
		}
		return b
	}
	for range 2000 {
		values := make([]string, 1+r.IntN(3))
		for i := range values {
			values[i] = string(random(1 + r.IntN(4)))
		}
		input := random(r.IntN(40))
		want := wholeInputMask(toBytes(values), input)
		var chunks [][]byte
		for rest := input; len(rest) > 0; {
			n := 1 + r.IntN(len(rest))
			chunks, rest = append(chunks, rest[:n]), rest[n:]
		}
		got := mask(t, values, chunks...)
		if !bytes.Equal(got, want) {
			t.Fatalf("values %q input %q chunks %q: %q, want %q", values, input, chunks, got, want)
		}
		for _, value := range values {
			if bytes.Contains(got, []byte(value)) {
				t.Fatalf("values %q input %q: output %q contains %q", values, input, got, value)
			}
		}
	}
}

// wholeInputMask is the reference: mark every byte inside any occurrence,
// then collapse each marked run into one marker.
func wholeInputMask(values [][]byte, input []byte) []byte {
	marked := make([]bool, len(input))
	for _, v := range values {
		for i := 0; i+len(v) <= len(input); i++ {
			if bytes.Equal(input[i:i+len(v)], v) {
				for j := i; j < i+len(v); j++ {
					marked[j] = true
				}
			}
		}
	}
	marker := Marker(values)
	var out []byte
	for i, b := range input {
		switch {
		case !marked[i]:
			out = append(out, b)
		case i == 0 || !marked[i-1]:
			out = append(out, marker...)
		}
	}
	return out
}

func TestOutputIsIncremental(t *testing.T) {
	var out bytes.Buffer
	w, err := NewWriter(&out, [][]byte{[]byte("0123456789")})
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct{ write, passed string }{
		// A prompt that cannot begin the value passes at once.
		{"Password: ", "Password: "},
		// A trailing part that begins the value waits for what follows.
		{"x 012", "Password: x "},
		{"3x", "Password: x 0123x"},
		{"01234", "Password: x 0123x"},
		{"56789!", "Password: x 0123x***!"},
	} {
		if _, err := w.Write([]byte(step.write)); err != nil {
			t.Fatal(err)
		}
		if out.String() != step.passed {
			t.Fatalf("after writing %q: passed %q, want %q", step.write, out.String(), step.passed)
		}
	}
	// A long stream that never begins the value is never held back.
	for range 100 {
		w.Write(bytes.Repeat([]byte("x"), 1000))
	}
	if held := len("Password: x 0123x***!") + 100*1000 - out.Len(); held != 0 {
		t.Fatalf("%d bytes held back", held)
	}
}

type failingWriter struct{ writes int }

func (f *failingWriter) Write(p []byte) (int, error) {
	f.writes++
	return 0, errors.New("broken pipe")
}

func TestWriteFailureStopsOutput(t *testing.T) {
	f := &failingWriter{}
	w, err := NewWriter(f, [][]byte{[]byte("v")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("first")); err == nil {
		t.Fatal("the first write succeeded")
	}
	if _, err := w.Write([]byte("second")); err == nil {
		t.Fatal("a write after a failure succeeded")
	}
	if err := w.Close(); err == nil {
		t.Fatal("Close after a failure succeeded")
	}
	if f.writes != 1 {
		t.Fatalf("%d writes reached the failing writer, want 1", f.writes)
	}
}

// A NUL in a value leaves no byte for the marker.
func TestRejectsValueWithNUL(t *testing.T) {
	if _, err := NewWriter(&bytes.Buffer{}, toBytes([]string{"a\x00b"})); err == nil {
		t.Error("NewWriter accepted a value with NUL")
	}
}
