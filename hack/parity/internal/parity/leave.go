package parity

import (
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
)

// Leave is the tests a full record leaves to another record, which it
// neither needs to have run nor retires. ParseLeave is the only way to fill
// one, and the zero Leave holds none.
type Leave struct {
	ids map[string]bool
}

// holds reports whether l names the test id.
func (l Leave) holds(id string) bool {
	return l.ids[id]
}

// count returns how many of ledger's tests l names, none for a nil ledger.
func (l Leave) count(ledger *Ledger) int {
	if ledger == nil {
		return 0
	}
	n := 0
	for id := range ledger.Tests {
		if l.holds(id) {
			n++
		}
	}
	return n
}

// ParseLeave reads the tests a full record leaves: pytest node ids, one a
// line, blank lines aside, as hack/s3tests select --gate-only-ids writes the
// gate-only tests the selection resolved. It returns them by the ids
// pytest's junit report gives them, as ParseSelection does, and refuses a
// line that is not a node id, FILE.py::NAME. A list may name no test.
func ParseLeave(r io.Reader) (Leave, error) {
	ids, err := readNodeIDs(r)
	if err != nil {
		return Leave{}, fmt.Errorf("reading the leave list: %w", err)
	}
	return Leave{ids: ids}, nil
}

// leaveProblem returns why a record of res cannot leave the tests opts.Leave
// names: a subset record retires nothing, so it has nothing to leave, and a
// test the runs ran is one their selection did not leave out, which a record
// that leaves it would carry no entry for. It names every such test.
func leaveProblem(res Result, opts RecordOptions) error {
	if len(opts.Leave.ids) == 0 {
		return nil
	}
	if opts.Subset {
		return errors.New("a subset record retires nothing, so it leaves no test to another record")
	}
	var errs []error
	for _, id := range slices.Sorted(maps.Keys(res.Outcomes)) {
		if opts.Leave.holds(id) {
			errs = append(errs, fmt.Errorf("%s: the runs ran it, and the leave list names it for another record", id))
		}
	}
	return errors.Join(errs...)
}
