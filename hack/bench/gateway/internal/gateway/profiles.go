package gateway

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jhoblitt/rgw-go/hack/internal/harness"
)

// requestTimeout is how long a request of elbencho's or s3send's may move no
// byte before it is an error: elbencho's --s3reqtimeout fails a request that
// transferred nothing for that long, and s3send's -request-timeout one that
// sent none of its body and received none of its answer, so that a gateway
// that stops answering some threads fails the phase in place of leaving a
// lower rate, and a slow one is measured.
const requestTimeout = 30 * time.Second

// el is elbencho against endpoint as bench, without live statistics.
func (st *state) el(endpoint string, args ...string) harness.Cmd {
	return harness.Cmd{
		Name: st.elbencho,
		Args: append([]string{
			"--s3endpoints", endpoint, "--s3key", benchKey, "--s3region", st.apiName,
			"--s3nocompress", "--s3reqtimeout", strconv.FormatInt(requestTimeout.Milliseconds(), 10), "--nolive",
		}, args...),
		Env: []string{"AWS_SECRET_ACCESS_KEY=" + benchSecret},
	}
}

// send is s3send against endpoint as bench; like elbencho's, its secret
// reaches it through the environment.
func (st *state) send(endpoint string, args ...string) harness.Cmd {
	return harness.Cmd{
		Name: st.s3send,
		Args: append([]string{
			"-endpoint", endpoint, "-key", benchKey, "-region", st.apiName, "-request-timeout", requestTimeout.String(),
		}, args...),
		Env: []string{"AWS_SECRET_ACCESS_KEY=" + benchSecret},
	}
}

// profileRun is one profile under way, with its log, <profile>.log, which
// takes every command's output.
type profileRun struct {
	r       *Runner
	st      *state
	name    string
	log     io.Writer
	logPath string
}

// runProfile runs profile p's preparations and phases.
func (r *Runner) runProfile(ctx context.Context, st *state, name string) (err error) {
	p := &profileRun{r: r, st: st, name: name, logPath: filepath.Join(st.dir, name+".log")}
	f, err := harness.OpenLog(p.logPath)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	p.log = f
	switch name {
	case "small-put":
		return p.smallPut(ctx)
	case "small-get":
		return p.smallGet(ctx)
	case "noop":
		return p.noop(ctx)
	case "large-put":
		return p.largePut(ctx)
	case "large-get":
		return p.largeGet(ctx)
	case "comp-small-put":
		return p.smallSendPut(ctx, "-storage-class", compClass, "-compressible")
	case "comp-small-get":
		return p.compSmallGet(ctx)
	case "comp-large-put":
		return p.compLargePut(ctx)
	case "comp-large-get":
		return p.compLargeGet(ctx)
	case "crc-put":
		return p.smallSendPut(ctx, "-checksum", "crc32")
	case "ver-put":
		return p.verPut(ctx)
	case "ver-get":
		return p.verGet(ctx)
	default:
		return p.list(ctx)
	}
}

// quiet leaves the gateway idle for two samples after a phase, so that the
// samples a phase's CPU is read between see little of its neighbors.
func (p *profileRun) quiet(ctx context.Context) error {
	return p.r.Sleep(ctx, p.r.Quiet)
}

// prep runs an unmeasured elbencho command against the gateway: a bucket to
// create, a set to write for a read phase, objects to remove. The quiet
// after it keeps its CPU out of the samples the next phase's is read
// between.
func (p *profileRun) prep(ctx context.Context, what string, args ...string) error {
	return p.prepCmd(ctx, what, p.st.el(p.st.endpoint, args...))
}

// prepSend is prep for an s3send command.
func (p *profileRun) prepSend(ctx context.Context, what string, args ...string) error {
	return p.prepCmd(ctx, what, p.st.send(p.st.endpoint, args...))
}

func (p *profileRun) prepCmd(ctx context.Context, what string, c harness.Cmd) error {
	p.st.log.InfoContext(ctx, "prep", slog.String("profile", p.name), slog.String("what", what))
	c.Out = p.log
	if _, err := harness.Output(ctx, p.r.Exec, p.r.PrepTimeout, c); err != nil {
		return fmt.Errorf("%s: %s failed; see %s: %w", p.name, what, p.logPath, err)
	}
	return p.quiet(ctx)
}

// phase runs one measured elbencho phase, appending its results to the
// profile's json and csv; last says it is the profile's last, which rgw-go
// is profiled through.
func (p *profileRun) phase(ctx context.Context, last bool, args ...string) error {
	p.st.log.InfoContext(ctx, "phase", slog.String("profile", p.name), slog.String("elbencho", strings.Join(args, " ")))
	c := p.st.el(p.st.endpoint, append([]string{
		"--lat", "--latpercent", "--latpercent9s", "1", "--lathisto",
		"--csvfile", filepath.Join(p.st.dir, p.name+".csv"), "--jsonfile", filepath.Join(p.st.dir, p.name+".json"),
	}, args...)...)
	return p.measure(ctx, last, c, "elbencho "+strings.Join(args, " "))
}

// sendPhase runs one measured s3send put bounded by BENCH_SECONDS, its result
// in <profile>-<label>.json; last is as phase's.
func (p *profileRun) sendPhase(ctx context.Context, last bool, label string, args ...string) error {
	p.st.log.InfoContext(ctx, "phase", slog.String("profile", p.name), slog.String("s3send", strings.Join(args, " ")))
	c := p.st.send(p.st.endpoint, append([]string{
		"-op", "put", "-duration", itoa(p.st.cfg.Seconds) + "s", "-out", filepath.Join(p.st.dir, p.name+"-"+label+".json"),
	}, args...)...)
	return p.measure(ctx, last, c, "s3send "+strings.Join(args, " "))
}

// measure runs c, a measured phase, taking rgw-go's CPU profile through it
// when it is the profile's last.
func (p *profileRun) measure(ctx context.Context, last bool, c harness.Cmd, what string) error {
	if last {
		p.r.startProfile(ctx, p.st, p.name)
	}
	c.Out = io.MultiWriter(p.log, p.r.Stdout)
	if _, err := harness.Output(ctx, p.r.Exec, p.r.phaseTimeout(p.st), c); err != nil {
		return fmt.Errorf("%s: %s failed: %w", p.name, what, err)
	}
	if last {
		if err := p.r.waitProfile(ctx, p.st); err != nil {
			return err
		}
	}
	return p.quiet(ctx)
}

// phaseTimeout bounds a measured phase, an httpload run and a CPU profile:
// BENCH_SECONDS, and PhaseMargin for elbencho's setup and the connections'
// end.
func (r *Runner) phaseTimeout(st *state) time.Duration {
	return time.Duration(st.cfg.Seconds)*time.Second + r.PhaseMargin
}

// itoa is n in decimal, as elbencho's and httpload's arguments take it.
func itoa(n int64) string {
	return strconv.FormatInt(n, 10)
}

// smallPut PUTs 4 KiB objects at each concurrency, BENCH_PUT_OBJECTS per
// thread.
func (p *profileRun) smallPut(ctx context.Context) error {
	cfg := p.st.cfg
	if err := p.r.needSpace(ctx, p.st, p.name); err != nil {
		return err
	}
	if err := p.prep(ctx, "creating bench-small", "-d", "-t", "1", "bench-small"); err != nil {
		return err
	}
	for i, t := range cfg.Conc {
		if err := p.phase(ctx, i == len(cfg.Conc)-1, "-w", "-t", itoa(t), "-n", "0", "-N", itoa(cfg.PutObjects),
			"-s", "4k", "-b", "4k", "--timelimit", itoa(cfg.Seconds), "bench-small"); err != nil {
			return err
		}
	}
	return nil
}

// smallGet GETs 4 KiB objects at each concurrency, looping over a read set
// written first.
func (p *profileRun) smallGet(ctx context.Context) error {
	cfg := p.st.cfg
	if err := p.r.needSpace(ctx, p.st, p.name); err != nil {
		return err
	}
	if err := p.prep(ctx, fmt.Sprintf("writing %d x %d objects of 4 KiB to read", cfg.Top(), cfg.GetObjects),
		"-d", "-w", "-t", itoa(cfg.Top()), "-n", "0", "-N", itoa(cfg.GetObjects), "-s", "4k", "-b", "4k", "bench-small"); err != nil {
		return err
	}
	return p.readSmall(ctx, "bench-small")
}

// readSmall GETs 4 KiB objects of bucket at each concurrency, looping over the
// read set already written.
func (p *profileRun) readSmall(ctx context.Context, bucket string) error {
	cfg := p.st.cfg
	for i, t := range cfg.Conc {
		if err := p.phase(ctx, i == len(cfg.Conc)-1, "-r", "-t", itoa(t), "-n", "0", "-N", itoa(cfg.GetObjects),
			"-s", "4k", "-b", "4k", "--timelimit", itoa(cfg.Seconds), "--infloop", bucket); err != nil {
			return err
		}
	}
	return nil
}

// noop runs httpload's anonymous GET / at each concurrency, which radosgw
// answers without touching RADOS.
func (p *profileRun) noop(ctx context.Context) error {
	cfg := p.st.cfg
	for i, t := range cfg.Conc {
		last := i == len(cfg.Conc)-1
		p.st.log.InfoContext(ctx, "httpload", slog.String("profile", p.name), slog.Int64("threads", t))
		if last {
			p.r.startProfile(ctx, p.st, p.name)
		}
		c := harness.Cmd{Name: p.st.httpload, Args: []string{
			"-url", p.st.endpoint + "/", "-threads", itoa(t), "-duration", itoa(cfg.Seconds) + "s",
			"-out", filepath.Join(p.st.dir, "noop-"+itoa(t)+".json"),
		}, Out: io.MultiWriter(p.log, p.r.Stdout)}
		if _, err := harness.Output(ctx, p.r.Exec, p.r.phaseTimeout(p.st), c); err != nil {
			return fmt.Errorf("noop: httpload at %d failed: %w", t, err)
		}
		if last {
			if err := p.r.waitProfile(ctx, p.st); err != nil {
				return err
			}
		}
		if err := p.quiet(ctx); err != nil {
			return err
		}
	}
	return nil
}

// largePut PUTs 64 MiB objects, whole and in 8 MiB parts, each write's
// objects removed after it.
func (p *profileRun) largePut(ctx context.Context) error {
	cfg := p.st.cfg
	if err := p.prep(ctx, "creating bench-large and bench-large-mpu", "-d", "-t", "1", "bench-large", "bench-large-mpu"); err != nil {
		return err
	}
	return p.largeWrites(ctx, func(w largeWrite) error {
		return p.phase(ctx, w.last, "-w", "-t", itoa(cfg.LargeThreads), "-n", "0", "-N", itoa(cfg.LargeObjects),
			"-s", "64m", "-b", w.block, "--timelimit", itoa(cfg.Seconds), w.bucket)
	})
}

// largeWrite is one of a large profile's two writes: 64 MiB objects whole,
// then in 8 MiB parts, which are the last measured, so rgw-go is profiled
// through them.
type largeWrite struct {
	bucket string
	// block is elbencho's -b: the object, or its parts.
	block string
	parts bool
	last  bool
}

// label names the write in the file of an s3send result.
func (w largeWrite) label() string {
	if w.parts {
		return "parts"
	}
	return "whole"
}

// largeWrites runs write once for each of the two, each time with the space
// it needs free and its objects removed after it.
func (p *profileRun) largeWrites(ctx context.Context, write func(largeWrite) error) error {
	for _, w := range []largeWrite{
		{"bench-large", "64m", false, false},
		{"bench-large-mpu", "8m", true, true},
	} {
		if err := p.r.needSpace(ctx, p.st, p.name); err != nil {
			return err
		}
		if err := write(w); err != nil {
			return err
		}
		if err := p.removeLarge(ctx, w.bucket); err != nil {
			return err
		}
	}
	return nil
}

// largeGet reads 64 MiB objects in 4 MiB ranges and whole, looping over a
// set written first.
func (p *profileRun) largeGet(ctx context.Context) error {
	cfg := p.st.cfg
	if err := p.r.needSpace(ctx, p.st, p.name); err != nil {
		return err
	}
	if err := p.prep(ctx, fmt.Sprintf("writing %d x %d objects of 64 MiB to read", cfg.LargeThreads, cfg.LargeObjects),
		"-d", "-w", "-t", itoa(cfg.LargeThreads), "-n", "0", "-N", itoa(cfg.LargeObjects), "-s", "64m", "-b", "64m", "bench-large"); err != nil {
		return err
	}
	return p.readLarge(ctx)
}

// readLarge reads the 64 MiB objects of bench-large in 4 MiB ranges and
// whole, looping over the set already written, and removes them.
func (p *profileRun) readLarge(ctx context.Context) error {
	cfg := p.st.cfg
	for _, read := range []struct {
		block string
		last  bool
	}{{"4m", false}, {"64m", true}} {
		if err := p.phase(ctx, read.last, "-r", "-t", itoa(cfg.LargeThreads), "-n", "0", "-N", itoa(cfg.LargeObjects),
			"-s", "64m", "-b", read.block, "--timelimit", itoa(cfg.Seconds), "--infloop", "bench-large"); err != nil {
			return err
		}
	}
	return p.removeLarge(ctx, "bench-large")
}

// smallSendPut PUTs 4 KiB objects with s3send at each concurrency,
// BENCH_PUT_OBJECTS per thread, with the s3send flags extra adds: the storage
// class or the checksum that elbencho cannot send.
func (p *profileRun) smallSendPut(ctx context.Context, extra ...string) error {
	cfg := p.st.cfg
	if err := p.r.needSpace(ctx, p.st, p.name); err != nil {
		return err
	}
	if err := p.prep(ctx, "creating bench-small", "-d", "-t", "1", "bench-small"); err != nil {
		return err
	}
	for i, t := range cfg.Conc {
		args := []string{"-bucket", "bench-small", "-threads", itoa(t), "-objects", itoa(cfg.PutObjects), "-size", itoa(objSmall)}
		if err := p.sendPhase(ctx, i == len(cfg.Conc)-1, itoa(t), append(args, extra...)...); err != nil {
			return err
		}
	}
	return nil
}

// compSmallGet GETs 4 KiB objects at each concurrency, looping over a read
// set s3send wrote through compClass first; the reads are small-get's.
func (p *profileRun) compSmallGet(ctx context.Context) error {
	cfg := p.st.cfg
	if err := p.r.needSpace(ctx, p.st, p.name); err != nil {
		return err
	}
	if err := p.prep(ctx, "creating bench-small", "-d", "-t", "1", "bench-small"); err != nil {
		return err
	}
	if err := p.prepSend(ctx, fmt.Sprintf("writing %d x %d objects of 4 KiB through %s to read", cfg.Top(), cfg.GetObjects, compClass),
		"-op", "put", "-bucket", "bench-small", "-threads", itoa(cfg.Top()), "-objects", itoa(cfg.GetObjects), "-size", itoa(objSmall),
		"-storage-class", compClass, "-compressible"); err != nil {
		return err
	}
	return p.readSmall(ctx, "bench-small")
}

// compLargePut PUTs 64 MiB objects through compClass, whole and in 8 MiB
// parts, each write's objects removed after it.
func (p *profileRun) compLargePut(ctx context.Context) error {
	cfg := p.st.cfg
	if err := p.prep(ctx, "creating bench-large and bench-large-mpu", "-d", "-t", "1", "bench-large", "bench-large-mpu"); err != nil {
		return err
	}
	return p.largeWrites(ctx, func(w largeWrite) error {
		args := []string{
			"-bucket", w.bucket, "-threads", itoa(cfg.LargeThreads), "-objects", itoa(cfg.LargeObjects), "-size", itoa(objLarge),
			"-storage-class", compClass, "-compressible",
		}
		if w.parts {
			args = append(args, "-block", itoa(objLargePart))
		}
		return p.sendPhase(ctx, w.last, w.label(), args...)
	})
}

// compLargeGet reads 64 MiB objects in 4 MiB ranges and whole, looping over a
// set s3send wrote through compClass first; the reads are large-get's.
func (p *profileRun) compLargeGet(ctx context.Context) error {
	cfg := p.st.cfg
	if err := p.r.needSpace(ctx, p.st, p.name); err != nil {
		return err
	}
	if err := p.prep(ctx, "creating bench-large", "-d", "-t", "1", "bench-large"); err != nil {
		return err
	}
	if err := p.prepSend(ctx, fmt.Sprintf("writing %d x %d objects of 64 MiB through %s to read", cfg.LargeThreads, cfg.LargeObjects, compClass),
		"-op", "put", "-bucket", "bench-large", "-threads", itoa(cfg.LargeThreads), "-objects", itoa(cfg.LargeObjects), "-size", itoa(objLarge),
		"-storage-class", compClass, "-compressible"); err != nil {
		return err
	}
	return p.readLarge(ctx)
}

// versionedBucket makes verBucket and turns its versioning on, which
// elbencho cannot leave on.
func (p *profileRun) versionedBucket(ctx context.Context) error {
	if err := p.prep(ctx, "creating "+verBucket, "-d", "-t", "1", verBucket); err != nil {
		return err
	}
	return p.prepSend(ctx, "enabling versioning on "+verBucket, "-op", "versioning", "-bucket", verBucket)
}

// verPut PUTs 4 KiB objects into the versioned bucket at each concurrency,
// Config.VerObjects per thread. A phase's keys, by thread and index, include
// the earlier phases', whose threads are fewer: of a phase's PUTs the share
// its predecessor's threads make of its own are of keys the previous phase may
// have written, new versions if it did before its time limit, and the rest
// write a key for the first time. A run of one concurrency writes no new
// version, so what ver-put measures is that mix.
func (p *profileRun) verPut(ctx context.Context) error {
	cfg := p.st.cfg
	if err := p.r.needSpace(ctx, p.st, p.name); err != nil {
		return err
	}
	if err := p.versionedBucket(ctx); err != nil {
		return err
	}
	for i, t := range cfg.Conc {
		if err := p.phase(ctx, i == len(cfg.Conc)-1, "-w", "-t", itoa(t), "-n", "0", "-N", itoa(cfg.VerObjects()),
			"-s", "4k", "-b", "4k", "--timelimit", itoa(cfg.Seconds), verBucket); err != nil {
			return err
		}
	}
	return nil
}

// verGet GETs 4 KiB objects of the versioned bucket at each concurrency,
// looping over a read set written first, each key at one version.
func (p *profileRun) verGet(ctx context.Context) error {
	cfg := p.st.cfg
	if err := p.r.needSpace(ctx, p.st, p.name); err != nil {
		return err
	}
	if err := p.versionedBucket(ctx); err != nil {
		return err
	}
	if err := p.prep(ctx, fmt.Sprintf("writing %d x %d objects of 4 KiB to read", cfg.Top(), cfg.GetObjects),
		"-w", "-t", itoa(cfg.Top()), "-n", "0", "-N", itoa(cfg.GetObjects), "-s", "4k", "-b", "4k", verBucket); err != nil {
		return err
	}
	return p.readSmall(ctx, verBucket)
}

// list lists BENCH_LIST_THREADS * BENCH_LIST_OBJECTS empty keys with
// ListObjectsV2, from one thread and with one prefix per thread.
func (p *profileRun) list(ctx context.Context) error {
	cfg := p.st.cfg
	keys := cfg.ListThreads * cfg.ListObjects
	if err := p.prep(ctx, fmt.Sprintf("writing %d empty objects to list", keys),
		"-d", "-w", "-t", itoa(cfg.ListThreads), "-n", "0", "-N", itoa(cfg.ListObjects), "-s", "0", "bench-list"); err != nil {
		return err
	}
	if err := p.phase(ctx, false, "--s3listobj", itoa(keys), "-t", "1", "--timelimit", itoa(cfg.Seconds), "--infloop", "bench-list"); err != nil {
		return err
	}
	return p.phase(ctx, true, "--s3listobjpar", "-t", itoa(cfg.ListThreads), "-n", "0", "-N", itoa(cfg.ListObjects),
		"--timelimit", itoa(cfg.Seconds), "--infloop", "bench-list")
}

// removeLarge removes a large write's objects and collects their tails,
// since the parity options leave nothing else collecting them.
func (p *profileRun) removeLarge(ctx context.Context, bucket string) error {
	cfg := p.st.cfg
	if err := p.prep(ctx, "removing the objects of "+bucket,
		"-F", "-t", itoa(cfg.LargeThreads), "-n", "0", "-N", itoa(cfg.LargeObjects), "--nodelerr", bucket); err != nil {
		return err
	}
	if err := p.r.gcProcess(ctx, p.st, p.log); err != nil {
		return fmt.Errorf("%s: radosgw-admin gc process failed: %w", p.name, err)
	}
	return nil
}

// startProfile takes rgw-go's CPU profile over the next BENCH_SECONDS in the
// background, and waitProfile waits for it; a radosgw run takes none.
// run.log records when each starts and when it has ended.
func (r *Runner) startProfile(ctx context.Context, st *state, profile string) {
	if st.cfg.Gateway != "rgw-go" {
		return
	}
	st.out.InfoContext(ctx, "taking the CPU profile", slog.String("profile", profile), slog.Int64("seconds", st.cfg.Seconds))
	fetch, cancel := context.WithTimeout(ctx, r.phaseTimeout(st))
	done := make(chan error, 1)
	go func() { done <- r.fetchProfile(fetch, st, profile) }()
	st.profile = &running{end: cancel, done: done}
}

func (r *Runner) waitProfile(ctx context.Context, st *state) error {
	if st.profile == nil {
		return nil
	}
	err := st.profile.finish()
	st.profile = nil
	if err != nil {
		return fmt.Errorf("taking rgw-go's CPU profile failed: %w", err)
	}
	st.out.InfoContext(ctx, "took the CPU profile")
	return nil
}

// fetchProfile writes rgw-go's CPU profile over BENCH_SECONDS, from its
// metrics address's /debug/pprof/profile, to cpu-<profile>.pprof.
func (r *Runner) fetchProfile(ctx context.Context, st *state, profile string) (err error) {
	url := "http://" + st.metrics + "/debug/pprof/profile?seconds=" + itoa(st.cfg.Seconds)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return fmt.Errorf("requesting %s: %w", url, err)
	}
	resp, err := r.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("requesting %s: %w", url, err)
	}
	defer resp.Body.Close() //nolint:errcheck // read to its end below
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("requesting %s: %s", url, resp.Status)
	}
	path := filepath.Join(st.dir, "cpu-"+profile+".pprof")
	f, err := os.Create(path) //nolint:gosec // the path is in the run directory
	if err != nil {
		return fmt.Errorf("creating %s: %w", path, err)
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	if _, err := io.Copy(f, resp.Body); err != nil {
		return fmt.Errorf("reading %s: %w", url, err)
	}
	return nil
}
