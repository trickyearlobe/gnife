// Package cli holds output, exit-code and concurrency helpers shared by the
// commands.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"sync"
)

// Exit codes.
const (
	ExitOK      = 0
	ExitFailure = 1
	ExitUsage   = 2
	ExitPartial = 3 // some items failed; details were reported
)

// ExitError carries a specific exit code up to main.
type ExitError struct {
	Code int
	Err  error
}

func (e *ExitError) Error() string {
	if e.Err == nil {
		return fmt.Sprintf("exit %d", e.Code)
	}
	return e.Err.Error()
}
func (e *ExitError) Unwrap() error { return e.Err }

// Partial wraps an error so main exits with ExitPartial.
func Partial(err error) error { return &ExitError{Code: ExitPartial, Err: err} }

// Usage wraps an error so main exits with ExitUsage.
func Usage(err error) error { return &ExitError{Code: ExitUsage, Err: err} }

// ExitCode maps an error to a process exit code.
func ExitCode(err error) int {
	if err == nil {
		return ExitOK
	}
	var ee *ExitError
	if errors.As(err, &ee) {
		return ee.Code
	}
	return ExitFailure
}

// PrintJSON writes v as indented JSON followed by a newline. json.RawMessage
// values are re-indented rather than dumped verbatim.
func PrintJSON(w io.Writer, v any) error {
	if raw, ok := v.(json.RawMessage); ok {
		var any1 any
		if err := json.Unmarshal(raw, &any1); err == nil {
			v = any1
		}
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

// PrintNames writes one name per line, sorted.
func PrintNames(w io.Writer, names []string) {
	sorted := append([]string(nil), names...)
	sort.Strings(sorted)
	for _, n := range sorted {
		fmt.Fprintln(w, n)
	}
}

// Warnf writes a warning to stderr.
func Warnf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "warning: "+format+"\n", args...)
}

// ItemError is one failed item from a bulk operation.
type ItemError struct {
	Item string `json:"item"`
	Err  string `json:"error"`
}

// ItemErrors is the collected failures of a bulk operation.
type ItemErrors []ItemError

func (e ItemErrors) Error() string {
	switch len(e) {
	case 0:
		return "no errors"
	case 1:
		return fmt.Sprintf("%s: %s", e[0].Item, e[0].Err)
	}
	return fmt.Sprintf("%d items failed (first: %s: %s)", len(e), e[0].Item, e[0].Err)
}

// Partial returns a Partial error, or nil when there were no failures.
func (e ItemErrors) Partial() error {
	if len(e) == 0 {
		return nil
	}
	return Partial(e)
}

// Report writes each failure to stderr as a JSON line and returns a
// Partial error, or nil when there were no failures.
func (e ItemErrors) Report() error {
	if len(e) == 0 {
		return nil
	}
	enc := json.NewEncoder(os.Stderr)
	for _, ie := range e {
		_ = enc.Encode(ie)
	}
	return Partial(e)
}

// ForEach runs fn over items with at most workers goroutines. Errors are
// collected per item rather than aborting the batch; ctx cancellation stops
// scheduling new items.
func ForEach[T any](ctx context.Context, workers int, items []T, name func(T) string, fn func(context.Context, T) error) ItemErrors {
	if workers < 1 {
		workers = 1
	}
	var (
		mu   sync.Mutex
		errs ItemErrors
		wg   sync.WaitGroup
		sem  = make(chan struct{}, workers)
	)
	for _, it := range items {
		if ctx.Err() != nil {
			break
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(it T) {
			defer wg.Done()
			defer func() { <-sem }()
			if err := fn(ctx, it); err != nil {
				mu.Lock()
				errs = append(errs, ItemError{Item: name(it), Err: err.Error()})
				mu.Unlock()
			}
		}(it)
	}
	wg.Wait()
	sort.Slice(errs, func(i, j int) bool { return errs[i].Item < errs[j].Item })
	return errs
}
