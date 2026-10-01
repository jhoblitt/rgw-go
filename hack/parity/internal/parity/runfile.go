package parity

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// readFile reads a file the command line named.
func readFile(path string) ([]byte, error) {
	return os.ReadFile(path) //nolint:gosec // reading the files named on its command line is the command's purpose
}

// severity orders the outcomes for a test a run file reports more than once.
var severity = map[string]int{outcomePassed: 0, outcomeSkipped: 1, outcomeFailed: 2, outcomeError: 3}

// merge sets id's outcome to o unless outcomes already holds a more severe
// one for it.
func merge(outcomes map[string]string, id, o string) {
	if prev, ok := outcomes[id]; ok && severity[prev] >= severity[o] {
		return
	}
	outcomes[id] = o
}

type junitCase struct {
	Classname string    `xml:"classname,attr"`
	Name      string    `xml:"name,attr"`
	Failure   *struct{} `xml:"failure"`
	Error     *struct{} `xml:"error"`
	Skipped   *struct{} `xml:"skipped"`
}

func (c *junitCase) outcome() string {
	switch {
	case c.Error != nil:
		return outcomeError
	case c.Failure != nil:
		return outcomeFailed
	case c.Skipped != nil:
		return outcomeSkipped
	default:
		return outcomePassed
	}
}

// readJunit reads the testcase elements of a junit report wherever they sit,
// under a testsuites root or a bare testsuite. pytest reports a test whose
// call failed and whose teardown then errored as two testcases with one id,
// a failure and an error, so the more severe outcome of a repeated id wins.
func readJunit(path string) (map[string]string, error) {
	data, err := readFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading the run file: %w", err)
	}

	outcomes := map[string]string{}
	dec := xml.NewDecoder(bytes.NewReader(data))
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return outcomes, nil
		}
		if err != nil {
			return nil, fmt.Errorf("parsing %s: %w", path, err)
		}
		start, ok := tok.(xml.StartElement)
		if !ok || start.Name.Local != "testcase" {
			continue
		}
		var c junitCase
		if err := dec.DecodeElement(&c, &start); err != nil {
			return nil, fmt.Errorf("parsing %s: %w", path, err)
		}
		merge(outcomes, c.Classname+"::"+c.Name, c.outcome())
	}
}

type testEvent struct {
	Action string
	Test   string
}

// readGoTest reads the terminal events of the tests in a go test -json
// stream, package events aside. A test that ran subtests passes or fails
// with them, so only its subtests are kept, unless it failed while none of
// them failed: then the failure is its own, a suite's setup or teardown.
func readGoTest(path string) (map[string]string, error) {
	data, err := readFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading the run file: %w", err)
	}

	all := map[string]string{}
	line := 0
	for text := range bytes.Lines(data) {
		line++
		if len(bytes.TrimSpace(text)) == 0 {
			continue
		}
		var ev testEvent
		if err := json.Unmarshal(text, &ev); err != nil {
			return nil, fmt.Errorf("parsing %s:%d: %w", path, line, err)
		}
		if ev.Test == "" {
			continue
		}
		switch ev.Action {
		case "pass":
			merge(all, ev.Test, outcomePassed)
		case "fail":
			merge(all, ev.Test, outcomeFailed)
		case "skip":
			merge(all, ev.Test, outcomeSkipped)
		}
	}

	outcomes := map[string]string{}
	for id, o := range all {
		subtests, subtestFailed := false, false
		for other, so := range all {
			if strings.HasPrefix(other, id+"/") {
				subtests = true
				subtestFailed = subtestFailed || so == outcomeFailed
			}
		}
		if !subtests || (o == outcomeFailed && !subtestFailed) {
			outcomes[id] = o
		}
	}
	return outcomes, nil
}
