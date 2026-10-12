package parity

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
)

// The s3-tests pin is data: the PIN file beside run.sh, exactly 40
// lowercase hex digits and a newline, is the single place it is set, and
// run.sh and parity read it the same way, by that one rule.

// pinLine is the one line of run.sh that sets S3TESTS_COMMIT: to the
// environment's, for a local try, or else to pin, which run.sh reads from
// the PIN file by parsePin's rule.
const pinLine = "readonly S3TESTS_COMMIT=${S3TESTS_COMMIT:-${pin}}"

// parsePin reads a PIN file's bytes: exactly 40 lowercase hex digits and a
// newline, nothing before or after them. file names it in errors.
func parsePin(data []byte, file string) (string, error) {
	if len(data) != 41 || data[40] != '\n' || !reCommitID.Match(data[:40]) {
		return "", fmt.Errorf("%s is not exactly 40 lowercase hex digits and a newline", file)
	}
	return string(data[:40]), nil
}

// readPinFile reads the PIN file at file, which must be a regular file, as
// run.sh requires.
func readPinFile(file string) (string, error) {
	info, err := os.Lstat(file)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "", fmt.Errorf("no PIN file %s", file)
	case err != nil:
		return "", fmt.Errorf("reading %s: %w", file, err)
	case !info.Mode().IsRegular():
		return "", fmt.Errorf("%s is not a regular file", file)
	}
	data, err := readFile(file)
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", file, err)
	}
	return parsePin(data, file)
}

// pinSourceProblems returns the slips around the pin it finds in run.sh's
// text, file's: pinLine missing or doubled, the name S3TESTS_COMMIT outside
// pinLine and full-line comments other than as the expansion
// ${S3TESTS_COMMIT}, or a line continuation joining it from pieces. It reads
// text, not bash, so it catches such mistakes, not every way run.sh could
// set the commit.
func pinSourceProblems(text, file string) []string {
	var problems []string
	n := 0
	for i, line := range strings.Split(text, "\n") {
		switch {
		case line == pinLine:
			n++
		case strings.HasPrefix(strings.TrimLeft(line, " \t"), "#"):
		case strings.Contains(strings.ReplaceAll(line, "${S3TESTS_COMMIT}", ""), "S3TESTS_COMMIT"):
			problems = append(problems, fmt.Sprintf("%s: line %d names S3TESTS_COMMIT other than as ${S3TESTS_COMMIT}: %s", file, i+1, line))
		}
	}
	if n != 1 {
		problems = append(problems, fmt.Sprintf("%s: %d lines, not 1, are %q, the one way S3TESTS_COMMIT may be set", file, n, pinLine))
	}
	if strings.Count(text, "S3TESTS_COMMIT") != strings.Count(strings.ReplaceAll(text, "\\\n", ""), "S3TESTS_COMMIT") {
		problems = append(problems, file+": a line continuation joins S3TESTS_COMMIT from pieces")
	}
	return problems
}
