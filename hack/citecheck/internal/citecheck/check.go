package citecheck

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// Checker checks citations against a clone of ceph/ceph. It reads the clone
// only through git rev-parse, git config and git cat-file, and caches every
// ref it resolves and every file it reads, so that each is asked of git once.
type Checker struct {
	dir   string
	refs  map[string]resolved
	files map[string]object
}

// resolved is what a citation's ref names: a commit, or why it names none.
type resolved struct{ commit, problem string }

// reCommitID is a full or abbreviated commit id.
var reCommitID = regexp.MustCompile(`^[0-9a-fA-F]{4,40}$`)

// object is what git holds at a path in a commit: a file, another kind of
// object, such as a tree, or nothing.
type object struct {
	kind string
	f    *file
}

// NewChecker returns a Checker reading the git repository dir, which must be
// one, and not a partial clone: git cat-file fetches an object a partial
// clone lacks from its promisor remote and writes it into the clone.
func NewChecker(ctx context.Context, dir string) (*Checker, error) {
	if _, err := git(ctx, dir, nil, "rev-parse", "--git-dir"); err != nil {
		return nil, fmt.Errorf("%s is not a git repository: %w", dir, err)
	}
	why, err := partialClone(ctx, dir)
	if err != nil {
		return nil, err
	}
	if why != "" {
		return nil, fmt.Errorf("%s is a partial clone (%s), which git would fetch the objects it lacks into: "+
			"check against a full clone", dir, why)
	}
	return &Checker{dir: dir, refs: map[string]resolved{}, files: map[string]object{}}, nil
}

// partialClone returns which setting makes dir a partial clone, or "".
func partialClone(ctx context.Context, dir string) (string, error) {
	out, found, err := gitConfig(ctx, dir, "--get", "extensions.partialClone")
	switch {
	case err != nil:
		return "", err
	case found:
		return "extensions.partialClone is " + out, nil
	}
	out, found, err = gitConfig(ctx, dir, "--type=bool", "--get-regexp", `^remote\..*\.promisor$`)
	if err != nil || !found {
		return "", err
	}
	for line := range strings.Lines(out) {
		if key, value, _ := strings.Cut(strings.TrimSpace(line), " "); value == "true" {
			return key + " is true", nil
		}
	}
	return "", nil
}

// gitConfig runs git config in dir and returns its output and whether it
// found the setting.
func gitConfig(ctx context.Context, dir string, args ...string) (out string, found bool, err error) {
	out, err = git(ctx, dir, nil, append([]string{"config"}, args...)...)
	if e, ok := errors.AsType[*exec.ExitError](err); ok && e.ExitCode() == 1 {
		return "", false, nil
	}
	return strings.TrimSpace(out), err == nil, err
}

// formProblem returns why a citation's form is wrong, or "" when it is
// right: the checks that need no clone, that it is well formed, that its
// lines run forwards from 1 and that its path is clean.
func formProblem(cite Citation) string {
	switch {
	case cite.Problem != "":
		return cite.Problem
	case cite.From < 1:
		return "line 0 does not exist: lines count from 1"
	case cite.From > cite.To:
		return fmt.Sprintf("lines %d-%d run backwards", cite.From, cite.To)
	case !fs.ValidPath(cite.Path) || cite.Path == ".":
		return fmt.Sprintf("path %s is not a clean path from the repository's root", cite.Path)
	}
	return ""
}

// Check returns why a citation is wrong, or "" when it holds, whatever its
// form. An error means git could not answer, not that the citation is wrong.
func (c *Checker) Check(ctx context.Context, cite Citation) (string, error) {
	if problem := formProblem(cite); problem != "" {
		return problem, nil
	}
	ref, err := c.resolve(ctx, cite.Ref)
	if err != nil {
		return "", err
	}
	if ref.problem != "" {
		return ref.problem, nil
	}
	obj, err := c.read(ctx, ref.commit, cite.Path)
	if err != nil {
		return "", err
	}
	switch obj.kind {
	case "":
		return fmt.Sprintf("path %s does not exist at %s", cite.Path, cite.Ref), nil
	case "blob":
	default:
		return fmt.Sprintf("path %s is a %s, not a file, at %s", cite.Path, obj.kind, cite.Ref), nil
	}
	if n := obj.f.lines(); cite.To > n {
		return fmt.Sprintf("line %d is past the end of %s, which has %d lines at %s", cite.To, cite.Path, n, cite.Ref), nil
	}
	if cite.Symbol == "" {
		return "", nil
	}
	return obj.f.symbolProblem(cite.Symbol, cite.From, cite.To), nil
}

// resolve returns the commit ref names, or why it names none.
func (c *Checker) resolve(ctx context.Context, ref string) (resolved, error) {
	if r, ok := c.refs[ref]; ok {
		return r, nil
	}
	r, err := c.classify(ctx, ref)
	if err != nil {
		return resolved{}, err
	}
	c.refs[ref] = r
	return r, nil
}

// classify resolves ref when it is a tag or a commit id. A branch, a remote
// branch or a symbolic ref such as HEAD is refused, since what it names moves,
// and so is any other revision expression.
func (c *Checker) classify(ctx context.Context, ref string) (resolved, error) {
	name, found, err := c.revParse(ctx, ref, "--symbolic-full-name")
	if err != nil {
		return resolved{}, err
	}
	id, rev := reCommitID.MatchString(ref), ref
	switch {
	case !found && !id:
		return resolved{problem: fmt.Sprintf("ref %s names no tag or commit in %s", ref, c.dir)}, nil
	case !found:
		// An abbreviation unique among commits that a tree's or a blob's id
		// shares is ambiguous to a lookup with no hint that a commit is
		// meant; ^{commit} below gives that hint.
	case strings.HasPrefix(name, "refs/tags/"):
		rev = name
	case name != "":
		return resolved{problem: fmt.Sprintf("ref %s is %s, a branch or symbolic ref, which moves: cite a tag or a commit id",
			ref, name)}, nil
	case !id:
		return resolved{problem: fmt.Sprintf("ref %s is neither a tag nor a commit id", ref)}, nil
	}
	commit, found, err := c.revParse(ctx, rev+"^{commit}")
	switch {
	case err != nil:
		return resolved{}, err
	case !found && id && len(ref) < 40:
		return resolved{problem: fmt.Sprintf("commit id %s names no commit in %s, or abbreviates more than one", ref, c.dir)}, nil
	case !found:
		return resolved{problem: fmt.Sprintf("ref %s names no commit in %s", ref, c.dir)}, nil
	}
	return resolved{commit: commit}, nil
}

// revParse runs git rev-parse --verify --quiet on rev with flags and returns
// its output and whether rev resolved.
func (c *Checker) revParse(ctx context.Context, rev string, flags ...string) (out string, found bool, err error) {
	args := append(append([]string{"rev-parse", "--verify", "--quiet"}, flags...), "--end-of-options", rev)
	out, err = git(ctx, c.dir, nil, args...)
	if e, ok := errors.AsType[*exec.ExitError](err); ok && e.ExitCode() == 1 {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return strings.TrimSpace(out), true, nil
}

// read returns what commit holds at path p.
func (c *Checker) read(ctx context.Context, commit, p string) (object, error) {
	name := commit + ":" + p
	if obj, ok := c.files[name]; ok {
		return obj, nil
	}
	out, err := git(ctx, c.dir, strings.NewReader(name+"\n"), "cat-file", "--batch")
	if err != nil {
		return object{}, err
	}
	obj, err := parseBatch(out, name)
	if err != nil {
		return object{}, fmt.Errorf("reading %s: %w", name, err)
	}
	slog.DebugContext(ctx, "read cited path", slog.String("commit", commit), slog.String("path", p),
		slog.String("kind", obj.kind))
	c.files[name] = obj
	return obj, nil
}

// parseBatch reads git cat-file --batch's answer for one object name: a
// line "<name> missing", or "<oid> <type> <size>" and the object's bytes.
func parseBatch(out, name string) (object, error) {
	r := bufio.NewReader(strings.NewReader(out))
	head, err := r.ReadString('\n')
	if err != nil {
		return object{}, fmt.Errorf("git cat-file answered %q", out)
	}
	head = strings.TrimSuffix(head, "\n")
	if head == name+" missing" {
		return object{}, nil
	}
	fields := strings.Fields(head)
	if len(fields) != 3 {
		return object{}, fmt.Errorf("git cat-file answered %q", head)
	}
	obj := object{kind: fields[1]}
	if obj.kind != "blob" {
		return obj, nil
	}
	size, err := strconv.Atoi(fields[2])
	if err != nil {
		return object{}, fmt.Errorf("git cat-file answered %q", head)
	}
	data := make([]byte, size)
	if _, err := io.ReadFull(r, data); err != nil {
		return object{}, fmt.Errorf("git cat-file sent fewer than %d bytes: %w", size, err)
	}
	obj.f = newFile(string(data))
	return obj, nil
}

// git runs git in dir with stdin and returns its output.
func git(ctx context.Context, dir string, stdin io.Reader, args ...string) (string, error) {
	// Every value a citation wrote follows --end-of-options or goes in on
	// stdin, so that none is read as an option.
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...) //nolint:gosec // see above
	cmd.Stdin = stdin
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}
