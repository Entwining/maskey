package cli

import (
	"bufio"
	"bytes"
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/Entwining/maskey/internal/store"
)

// prompts are where mk asks a question and reads the answer: the
// controlling terminal, so that neither stdout nor stderr carries the
// question, or stdin and stderr where it cannot be opened. Callers ask only
// when stdin is a terminal: one opened on its own would let a caller
// without a terminal, such as an agent, leave a question waiting on the
// owner's.
func prompts(env *Env) (*os.File, io.Writer) {
	if tty, err := env.Terminal(); err == nil {
		return tty, tty
	}
	return env.Stdin, env.Stderr
}

// readValue reads ref from stdin or a hidden terminal prompt. Piped input
// follows withoutLineEnding; i is the zero-based position in a batch of n.
func readValue(ctx context.Context, env *Env, ref store.Ref, i, n int, fromStdin bool) ([]byte, error) {
	if !fromStdin {
		position := ""
		if n > 1 {
			position = fmt.Sprintf("%d of %d, ", i+1, n)
		}
		in, out := prompts(env)
		return readHidden(ctx, in, out, fmt.Sprintf("Value for %s (%sinput hidden): ", ref, position))
	}
	value, err := readUnlessCancelled(ctx, func() ([]byte, error) {
		value, err := io.ReadAll(env.Stdin)
		return withoutLineEnding(value), err
	})
	return value, readError(ctx, err)
}

// readHidden writes prompt on out and reads one line from the terminal in
// without echoing it.
func readHidden(ctx context.Context, in *os.File, out io.Writer, prompt string) ([]byte, error) {
	fd := int(in.Fd())
	// Cancellation can return before ReadPassword restores echo, so
	// restore the terminal state here as well.
	state, err := term.GetState(fd)
	if err != nil {
		return nil, inputError()
	}
	defer func() { _ = term.Restore(fd, state) }()
	fmt.Fprint(out, prompt)
	defer fmt.Fprintln(out)
	value, err := readUnlessCancelled(ctx, func() ([]byte, error) { return term.ReadPassword(fd) })
	return value, readError(ctx, err)
}

// readError is how a read of a value that ended with err failed: cancelled
// once ctx is done, whatever err says, and otherwise an input failure.
func readError(ctx context.Context, err error) error {
	switch {
	case ctx.Err() != nil:
		return &store.Error{Kind: store.KindCancelled, Message: "cancelled before any entry was written"}
	case err != nil:
		return inputError()
	}
	return nil
}

// removePassphrase asks on the terminal for the passphrase of the private
// key value, which is ref's, and returns the key without it. An SSH key,
// which mk run lends through an agent that cannot ask for the passphrase,
// is stored only without it. Any other key stays as it is when the owner
// answers with an empty line, and where mk cannot ask or cannot decrypt,
// mk keeps it and warns how to proceed.
func removePassphrase(cmd *cobra.Command, env *Env, ref store.Ref, value []byte, sshKey bool) ([]byte, error) {
	tty, err := env.Terminal()
	switch {
	case err != nil && sshKey:
		return nil, sshPassphraseError(ref)
	case err != nil:
		warn(cmd, env, "the private key is protected by a passphrase",
			fmt.Sprintf("mk set --stdin %s < <file> in a terminal offers to store the key without it; keep it when the program reads the passphrase from another entry", ref))
		return value, nil
	case sshKey:
		fmt.Fprintf(tty, "%s is protected by a passphrase, which mk run cannot ask for. Enter the passphrase to store the key without it, or press Ctrl-C to stop.\n", ref)
	default:
		fmt.Fprintf(tty, "%s is protected by a passphrase. Enter the passphrase to store the key without it, or press Return to store the key as it is.\n", ref)
	}
	prompt := "Passphrase (input hidden): "
	for {
		passphrase, err := readHidden(cmd.Context(), tty, tty, prompt)
		if err != nil {
			return nil, err
		}
		if len(passphrase) == 0 {
			if sshKey {
				continue
			}
			return value, nil
		}
		plain, err := withoutPassphrase(value, passphrase)
		switch {
		case errors.Is(err, x509.IncorrectPasswordError):
			prompt = "Bad passphrase, try again (input hidden): "
		case err != nil:
			const message = "mk cannot remove the passphrase from this key"
			const hint = "ssh-keygen -p -f <file> rewrites the key in OpenSSH format with a cipher mk reads; enter the same passphrase at its new-passphrase prompts to keep it, then retry; mk cannot remove the passphrase of a security key (sk-)"
			if sshKey {
				return nil, withHint(&store.Error{Kind: store.KindInvalid, Ref: &ref, Message: message}, hint)
			}
			warn(cmd, env, message, hint)
			return value, nil
		default:
			return plain, nil
		}
	}
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

// confirm asks prompt once on the terminal. Only a complete line answering y
// or yes confirms; otherwise, unchanged says what was left alone.
func confirm(ctx context.Context, env *Env, prompt, unchanged string) error {
	in, out := prompts(env)
	fmt.Fprint(out, prompt)
	answer, err := readUnlessCancelled(ctx, func() ([]byte, error) { return bufio.NewReader(in).ReadBytes('\n') })
	if !bytes.HasSuffix(answer, []byte("\n")) {
		// The error starts a line of its own after an answer cut short.
		fmt.Fprintln(out)
	}
	cancelled := &store.Error{Kind: store.KindCancelled, Message: unchanged}
	switch {
	case ctx.Err() != nil, errors.Is(err, io.EOF):
		return cancelled
	case err != nil:
		return withHint(&store.Error{Kind: kindIO, Message: "cannot read the answer"}, "check the input source; "+unchanged)
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
