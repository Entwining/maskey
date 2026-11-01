// Package keychain stores credentials as synchronizable generic-password
// items in the data protection Keychain, which iCloud Keychain syncs.
//
// The Security calls run in a helper hosted by Apple's /usr/bin/python3; see
// helper.py for why. Each operation starts one helper process and exchanges
// one request and one result with it over private pipes.
package keychain

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"math"
	"os/exec"
	"time"

	"github.com/Entwining/maskey/internal/activity"
	"github.com/Entwining/maskey/internal/store"
)

//go:embed helper.py
var helperSource string

// Interpreter is Apple's interpreter. Its absolute path keeps a python3
// earlier on PATH, which lacks Apple's application identifier, from hosting
// the helper.
const Interpreter = "/usr/bin/python3"

// OSStatus codes of the Security framework.
const (
	errSecUserCanceled          = -128
	errSecDuplicateItem         = -25299
	errSecItemNotFound          = -25300
	errSecNotAvailable          = -25291
	errSecInteractionNotAllowed = -25308
	errSecMissingEntitlement    = -34018
)

// Timeout bounds each run of the helper from its start, so that a helper
// that hangs ends the operation instead of holding it, and a service
// stopping after the owner's approval finishes its writes in time.
const Timeout = 30 * time.Second

type Keychain struct {
	service     string
	interpreter string
	timeout     time.Duration
	// recorder receives a record of every operation that touches a value
	// or program access.
	recorder activity.Recorder
}

func New(service string, recorder activity.Recorder) *Keychain {
	return &Keychain{service: service, interpreter: Interpreter, timeout: Timeout, recorder: recorder}
}

type request struct {
	Op       string         `json:"op"`
	Service  string         `json:"service"`
	Account  string         `json:"account,omitempty"`
	To       string         `json:"to,omitempty"`
	Accounts []string       `json:"accounts,omitempty"`
	Value    []byte         `json:"value,omitempty"`
	Details  *store.Details `json:"details,omitempty"`
}

type item struct {
	Account string `json:"account"`
	// Modified is in seconds since the Unix epoch.
	Modified      float64 `json:"modified"`
	ProgramAccess bool    `json:"program_access"`
	store.Details
}

func (i item) attributes() store.Attributes {
	attributes := store.Attributes{ProgramAccess: i.ProgramAccess, Details: i.Details}
	if i.Modified != 0 {
		seconds, fraction := math.Modf(i.Modified)
		attributes.Modified = time.Unix(int64(seconds), int64(fraction*1e9))
	}
	return attributes
}

type response struct {
	Ready  bool              `json:"ready"`
	Items  []item            `json:"items"`
	Value  []byte            `json:"value"`
	Values map[string][]byte `json:"values"`
	Error  *struct {
		Stage    string `json:"stage"`
		Message  string `json:"message"`
		OSStatus int32  `json:"os_status"`
	} `json:"error"`
}

func (k *Keychain) Items(ctx context.Context) ([]store.Item, error) {
	result, err := k.call(ctx, request{Op: "items"})
	if err != nil {
		return nil, err
	}
	items := make([]store.Item, len(result.Items))
	for i, item := range result.Items {
		items[i] = store.Item{Account: item.Account, Attributes: item.attributes()}
	}
	return items, nil
}

func (k *Keychain) Read(ctx context.Context, accounts []string) (map[string][]byte, error) {
	result, err := k.call(ctx, request{Op: "read", Accounts: accounts})
	if err != nil {
		return nil, err
	}
	return result.Values, nil
}

// Get leaves the decision to the helper, so that the value of an entry
// without program access never leaves it.
func (k *Keychain) Get(ctx context.Context, account string) ([]byte, error) {
	result, err := k.call(ctx, request{Op: "get", Account: account})
	if err != nil {
		return nil, err
	}
	return result.Value, nil
}

func (k *Keychain) Add(ctx context.Context, account string, value []byte, details store.Details) error {
	_, err := k.call(ctx, request{Op: "add", Account: account, Value: value, Details: &details})
	return err
}

func (k *Keychain) Update(ctx context.Context, account string, value []byte) error {
	_, err := k.call(ctx, request{Op: "update", Account: account, Value: value})
	return err
}

func (k *Keychain) Edit(ctx context.Context, account, to string, value []byte, details *store.Details) error {
	_, err := k.call(ctx, request{Op: "edit", Account: account, To: to, Value: value, Details: details})
	return err
}

func (k *Keychain) SetProgramAccess(ctx context.Context, account string, granted bool) error {
	op := "revoke"
	if granted {
		op = "grant"
	}
	_, err := k.call(ctx, request{Op: op, Account: account})
	return err
}

func (k *Keychain) Delete(ctx context.Context, account string) error {
	_, err := k.call(ctx, request{Op: "delete", Account: account})
	return err
}

// Check starts the helper and confirms that it runs under Apple's
// interpreter, without a Keychain call.
func (k *Keychain) Check(ctx context.Context) error {
	_, err := k.call(ctx, request{Op: "check"})
	return err
}

// call runs req and enters it in the activity record.
func (k *Keychain) call(ctx context.Context, req request) (*response, error) {
	result, err := k.exchange(ctx, req)
	k.record(ctx, req, result, err)
	return result, err
}

func (k *Keychain) record(ctx context.Context, req request, result *response, err error) {
	outcome := "ok"
	if err != nil {
		outcome = string(store.KindOf(err))
	}
	switch {
	case !Records(req.Op):
		return
	case store.IsKind(err, store.KindUnavailable):
		// It reached no item. Where mk falls back to the Keychain service,
		// the service records the operation for this process.
		return
	case req.Op == "add" && store.IsKind(err, store.KindExists):
		// It wrote nothing, so that a replacing mk set reads as the update
		// that follows.
		return
	case req.Op == "read":
		records := make([]activity.Record, len(req.Accounts))
		for i, account := range req.Accounts {
			outcome := outcome
			if err == nil && result.Values[account] == nil {
				outcome = string(store.KindNotFound)
			}
			records[i] = activity.Record{Op: req.Op, Entry: account, Result: outcome}
		}
		k.recorder.Record(ctx, records...)
		return
	}
	r := activity.Record{Op: req.Op, Entry: req.Account, Result: outcome}
	if req.Op == "edit" && req.To != req.Account {
		r.To = req.To
	}
	k.recorder.Record(ctx, r)
}

func (k *Keychain) exchange(ctx context.Context, req request) (*response, error) {
	req.Service = k.service
	input, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	// The request travels on stdin so no value appears in arguments. The
	// minimal environment keeps variables such as DEVELOPER_DIR from
	// redirecting /usr/bin/python3 to another interpreter.
	run, cancel := context.WithTimeout(ctx, k.timeout)
	defer cancel()
	cmd := exec.CommandContext(run, k.interpreter, "-I", "-S", "-c", helperSource)
	cmd.Env = []string{"PATH=/usr/bin:/bin"}
	cmd.Stdin = bytes.NewReader(input)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	// stderr stays unread: Python can quote the request in what it prints.
	runErr := cmd.Run()
	if ctx.Err() == nil && run.Err() != nil {
		runErr = fmt.Errorf("stopped after %v", k.timeout)
	}

	write := Writes(req.Op)
	ready, result := parse(stdout.Bytes())
	// A result line means the helper finished the request, so its outcome
	// stands even if the context ended while the helper exited.
	switch {
	case ctx.Err() != nil && result == nil && ready && write:
		return nil, &store.Error{Kind: store.KindUncertain, Message: "the write was cancelled after it started; check the entry before repeating it"}
	case ctx.Err() != nil && result == nil:
		return nil, &store.Error{Kind: store.KindCancelled, Message: "cancelled"}
	case !ready && result != nil && result.Error != nil && result.Error.Stage == "host":
		return nil, &store.Error{Kind: store.KindPrerequisite, Message: fmt.Sprintf("%s cannot host the Keychain helper: %s", k.interpreter, result.Error.Message)}
	case !ready:
		return nil, &store.Error{Kind: store.KindPrerequisite, Message: fmt.Sprintf("%s did not start (%v); install Apple's Command Line Tools with `xcode-select --install`", k.interpreter, runErr)}
	case result == nil && write:
		return nil, &store.Error{Kind: store.KindUncertain, Message: fmt.Sprintf("the Keychain helper ended without a result (%v); check the entry before repeating the write", runErr)}
	case result == nil:
		return nil, &store.Error{Kind: store.KindKeychain, Message: fmt.Sprintf("the Keychain helper ended without a result (%v)", runErr)}
	case result.Error != nil && result.Error.Stage == "keychain":
		return nil, statusError(result.Error.OSStatus)
	case result.Error != nil && result.Error.Stage == "not_granted":
		return nil, &store.Error{Kind: store.KindNotGranted, Message: "program access is not granted"}
	case result.Error != nil:
		return nil, &store.Error{Kind: store.KindKeychain, Message: "the Keychain helper rejected the request: " + result.Error.Message}
	}
	return result, nil
}

// Writes reports whether op may change an entry. An operation not listed as
// a read counts as a write, the safe guess.
func Writes(op string) bool {
	return op != "items" && op != "read" && op != "get" && op != "check"
}

// Records reports whether the activity record keeps op: metadata listing
// and the host check are not activity events.
func Records(op string) bool {
	return op != "items" && op != "check"
}

// parse reads the helper's output: a ready line, then a result line. A
// failed host check prints only its error.
func parse(output []byte) (ready bool, result *response) {
	lines := bytes.Split(output, []byte("\n"))
	var first response
	if json.Unmarshal(lines[0], &first) != nil {
		return false, nil
	}
	if !first.Ready {
		return false, &first
	}
	if len(lines) < 2 {
		return true, nil
	}
	var second response
	if json.Unmarshal(lines[1], &second) != nil || second.Ready {
		return true, nil
	}
	return true, &second
}

func statusError(status int32) error {
	e := &store.Error{OSStatus: status}
	switch status {
	case errSecItemNotFound:
		e.Kind, e.Message = store.KindNotFound, "the entry does not exist"
	case errSecDuplicateItem:
		e.Kind, e.Message = store.KindExists, "the entry already exists"
	case errSecInteractionNotAllowed:
		e.Kind, e.Message = store.KindLocked, "the Keychain is unavailable until this Mac is unlocked"
	case errSecNotAvailable:
		e.Kind, e.Message = store.KindUnavailable, "this process cannot reach the Keychain, as inside a sandbox that blocks it"
	case errSecMissingEntitlement:
		e.Kind, e.Message = store.KindPrerequisite, "the interpreter lacks the Keychain entitlement"
	case errSecUserCanceled:
		e.Kind, e.Message = store.KindCancelled, "the Keychain request was cancelled"
	default:
		e.Kind, e.Message = store.KindKeychain, "the Keychain returned an error"
	}
	return e
}

var _ store.Backend = (*Keychain)(nil)
