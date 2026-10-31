package cli

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"golang.org/x/term"

	"github.com/Entwining/maskey/internal/store"
)

// readValue reads ref from stdin or a hidden terminal prompt. Piped input
// follows withoutLineEnding; i is the zero-based position in a batch of n.
func readValue(ctx context.Context, env *Env, ref store.Ref, i, n int, fromStdin bool) ([]byte, error) {
	read := func() ([]byte, error) {
		value, err := io.ReadAll(env.Stdin)
		return withoutLineEnding(value), err
	}
	if !fromStdin {
		fd := int(env.Stdin.Fd())
		// Cancellation can return before ReadPassword restores echo, so
		// restore the terminal state here as well.
		state, err := term.GetState(fd)
		if err != nil {
			return nil, inputError()
		}
		defer func() { _ = term.Restore(fd, state) }()
		position := ""
		if n > 1 {
			position = fmt.Sprintf("%d of %d, ", i+1, n)
		}
		fmt.Fprintf(env.Stderr, "Value for %s (%sinput hidden): ", ref, position)
		defer fmt.Fprintln(env.Stderr)
		read = func() ([]byte, error) { return term.ReadPassword(fd) }
	}
	value, err := readUnlessCancelled(ctx, read)
	if ctx.Err() != nil {
		return nil, &store.Error{Kind: store.KindCancelled, Message: "cancelled before any entry was written"}
	}
	if err != nil {
		return nil, inputError()
	}
	return value, nil
}

func inputError() error {
	return withHint(&store.Error{Kind: kindIO, Message: "cannot read the value"}, "check the input source; no entry was written")
}

// withoutLineEnding drops the line ending of a value of one line, which
// producers such as echo add; a value of several lines, such as a PEM key,
// keeps every byte, its final newline included.
func withoutLineEnding(value []byte) []byte {
	if bytes.Count(value, []byte("\n")) != 1 || !bytes.HasSuffix(value, []byte("\n")) {
		return value
	}
	return bytes.TrimSuffix(value[:len(value)-1], []byte("\r"))
}

// confirmDelete asks once on the terminal to delete refs, which exist. Only
// a complete line answering y or yes confirms.
func confirmDelete(ctx context.Context, env *Env, refs []store.Ref) error {
	var prompt strings.Builder
	fmt.Fprintf(&prompt, "Delete %s from this Mac and Macs syncing this Keychain:\n", count(len(refs), "entry", "entries"))
	for _, ref := range refs {
		fmt.Fprintf(&prompt, "  %s\n", ref)
	}
	prompt.WriteString("This cannot be undone. Continue? [y/N] ")
	fmt.Fprint(env.Stderr, prompt.String())
	answer, err := readUnlessCancelled(ctx, func() ([]byte, error) { return bufio.NewReader(env.Stdin).ReadBytes('\n') })
	if !bytes.HasSuffix(answer, []byte("\n")) {
		// The error starts a line of its own after an answer cut short.
		fmt.Fprintln(env.Stderr)
	}
	cancelled := &store.Error{Kind: store.KindCancelled, Message: "no entries were deleted"}
	switch {
	case ctx.Err() != nil, errors.Is(err, io.EOF):
		return cancelled
	case err != nil:
		return withHint(&store.Error{Kind: kindIO, Message: "cannot read the answer"}, "check the input source; no entry was deleted")
	}
	switch strings.ToLower(strings.TrimSpace(string(answer))) {
	case "y", "yes":
		return nil
	}
	return cancelled
}

// readUnlessCancelled returns what read returns, or nothing once ctx is
// done: a read of a terminal or pipe does not end when the process is
// interrupted.
func readUnlessCancelled(ctx context.Context, read func() ([]byte, error)) ([]byte, error) {
	type outcome struct {
		value []byte
		err   error
	}
	done := make(chan outcome, 1)
	go func() {
		value, err := read()
		done <- outcome{value, err}
	}()
	select {
	case o := <-done:
		return o.value, o.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
