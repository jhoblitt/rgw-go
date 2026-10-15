package report

import (
	"cmp"
	"fmt"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
)

// gwRender renders the runs; baseline is the index of the run the others'
// ratios divide by, the first radosgw run, or -1.
type gwRender struct {
	runs     []*gwRun
	baseline int
}

func (g *gwRender) findings() []string {
	var out []string
	for _, r := range g.runs {
		for i := range r.phases {
			ph := &r.phases[i]
			if ph.errors == 0 && ph.retries.v == 0 {
				continue
			}
			f := fmt.Sprintf("%s, %s, %s at %d threads: %d errors", r.label, ph.profile, ph.variant, ph.threads, ph.errors)
			if ph.errorKinds != "" {
				f += " (" + ph.errorKinds + ")"
			}
			if ph.retries.ok {
				f += fmt.Sprintf(", %d retries", int64(ph.retries.v))
				if ph.retryKinds != "" {
					f += " (" + ph.retryKinds + ")"
				}
			}
			out = append(out, f)
		}
	}
	return out
}

func (g *gwRender) render(findings []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Gateway comparison: %s\n\n", g.runs[0].release)
	for _, r := range g.runs {
		fmt.Fprintf(&b, "- %s: %s; %s\n", r.label, r.dir, r.frontendsNote())
	}
	for _, r := range g.runs {
		if r.envString("smoke") == "1" {
			fmt.Fprintf(&b, "\n%s is a smoke run, BENCH_SMOKE=1: its phases prove the tooling and measure nothing.\n", r.label)
		}
	}
	b.WriteString("\nEach row is one measured phase. req/s counts S3 requests: elbencho's IOs, a listing's ListObjectsV2 " +
		"calls, httpload's GETs. A multipart PUT's row, marked (parts/s), counts as elbencho does: its req/s counts " +
		"UploadPart requests, leaving out each upload's Create and Complete, and its CPU per request is per part. " +
		"objects/s counts objects written or read, or keys listed. The latency percentiles are " +
		"per request, from elbencho's latency histogram as its --latpercent9s prints them, or httpload's. CPU per " +
		"request is the gateway process's user and system time over the phase, from the samples taken once a second " +
		"around it, interpolated at its start and end, so a short phase's is coarse; a 2 s gap after every phase and " +
		"every preparation keeps their CPU out of the next phase's. Peak RSS is the highest of those samples, and " +
		"thread high-water the most OS threads among them: Go parks an idle OS thread rather than ending it, so for " +
		"rgw-go it is the most the run has needed by the phase's end, its preparations included, not the phase's own " +
		"demand.")
	if g.baseline >= 0 {
		fmt.Fprintf(&b, " A ratio column divides a run's value by %s's.", g.runs[g.baseline].label)
	}
	b.WriteString("\n\n## Findings\n\n")
	if len(findings) == 0 {
		b.WriteString("No phase had an error or a retry.\n")
	}
	for _, f := range findings {
		fmt.Fprintf(&b, "- %s\n", f)
	}
	for _, t := range gwTables {
		g.renderTable(&b, t)
	}
	g.renderProfiles(&b)
	g.renderConfig(&b)
	g.renderEnv(&b)
	g.renderCommands(&b)
	return b.String()
}

// frontendsNote names the frontends the run's gateway served with and the
// TCP_NODELAY they gave its connections, as the gateway command recorded
// them.
func (r *gwRun) frontendsNote() string {
	frontends, noDelay := r.envString("frontends"), r.envString("tcp_nodelay")
	if frontends == "" && noDelay == "" {
		return "frontends and TCP_NODELAY not recorded"
	}
	quoted := "not recorded"
	if frontends != "" {
		quoted = fmt.Sprintf("%#q", frontends)
	}
	return fmt.Sprintf("frontends %s, TCP_NODELAY %s", quoted, cmp.Or(noDelay, "not recorded"))
}

// gwMetric is one measure of a phase, rendered as a column per run.
type gwMetric struct {
	name   string
	value  func(*gwPhase) opt
	format func(float64) string
	ratio  bool
}

func perSecond(n float64, ph *gwPhase) float64 { return n / ph.elapsed.Seconds() }

var latencyMetrics = []gwMetric{
	{"req/s", func(ph *gwPhase) opt { return some(perSecond(ph.requests, ph)) }, num, true},
	{"objects/s", func(ph *gwPhase) opt {
		if !ph.objects.ok {
			return opt{}
		}
		return some(perSecond(ph.objects.v, ph))
	}, num, true},
	{"MiB/s", func(ph *gwPhase) opt {
		if !ph.bytes.ok {
			return opt{}
		}
		return some(perSecond(ph.bytes.v, ph) / (1 << 20))
	}, num, true},
	{"p50 ms", func(ph *gwPhase) opt { return ms(ph.lat[0]) }, num, true},
	{"p99 ms", func(ph *gwPhase) opt { return ms(ph.lat[1]) }, num, true},
	{"p999 ms", func(ph *gwPhase) opt { return ms(ph.lat[2]) }, num, true},
}

var costMetrics = []gwMetric{
	{"CPU ms/req", func(ph *gwPhase) opt { return ph.cpuMsPerRequest }, num, true},
	{"thread high-water", func(ph *gwPhase) opt { return ph.peakThreads }, integer, true},
	{"peak RSS MiB", func(ph *gwPhase) opt {
		if !ph.peakRSSKiB.ok {
			return opt{}
		}
		return some(ph.peakRSSKiB.v / 1024)
	}, num, true},
	{"errors", func(ph *gwPhase) opt { return some(float64(ph.errors)) }, integer, false},
	{"retries", func(ph *gwPhase) opt { return ph.retries }, integer, false},
}

func ms(us opt) opt {
	if !us.ok {
		return us
	}
	return some(us.v / 1000)
}

func integer(v float64) string { return strconv.FormatInt(int64(math.Round(v)), 10) }

// gwRowKey is a table row: a variant at a concurrency.
type gwRowKey struct {
	variant string
	threads int
}

func (g *gwRender) renderTable(b *strings.Builder, t gwTable) {
	// Rows in order of concurrency, then of the variants as the runs list
	// them, so that each noop row sits beside small-get's at its concurrency.
	var variants []string
	rows := map[gwRowKey]bool{}
	for _, r := range g.runs {
		for i := range r.phases {
			ph := &r.phases[i]
			if !slices.Contains(t.profiles, ph.profile) {
				continue
			}
			if !slices.Contains(variants, ph.variant) {
				variants = append(variants, ph.variant)
			}
			rows[gwRowKey{ph.variant, ph.threads}] = true
		}
	}
	if len(rows) == 0 {
		return
	}
	keys := slices.SortedFunc(maps.Keys(rows), func(a, b gwRowKey) int {
		return cmp.Or(cmp.Compare(a.threads, b.threads),
			cmp.Compare(slices.Index(variants, a.variant), slices.Index(variants, b.variant)))
	})
	fmt.Fprintf(b, "\n## %s\n", t.name)
	g.renderMetrics(b, t, keys, "throughput and latency", latencyMetrics)
	g.renderMetrics(b, t, keys, "cost", costMetrics)
	g.renderGC(b, t)
}

func (g *gwRender) renderMetrics(b *strings.Builder, t gwTable, keys []gwRowKey, what string, metrics []gwMetric) {
	fmt.Fprintf(b, "\n### %s: %s\n\n| variant | threads |", t.name, what)
	cols := 2
	for _, m := range metrics {
		for i, r := range g.runs {
			fmt.Fprintf(b, " %s %s |", m.name, r.label)
			cols++
			if g.ratioAfter(m, i) {
				fmt.Fprintf(b, " %s/%s |", r.label, g.runs[g.baseline].label)
				cols++
			}
		}
	}
	b.WriteString("\n|" + strings.Repeat("---|", cols) + "\n")
	for _, k := range keys {
		fmt.Fprintf(b, "| %s | %d |", k.variant, k.threads)
		phases := make([]*gwPhase, len(g.runs))
		for i, r := range g.runs {
			phases[i] = r.phase(t, k)
		}
		for _, m := range metrics {
			for i := range g.runs {
				v := opt{}
				if phases[i] != nil {
					v = m.value(phases[i])
				}
				b.WriteString(" " + cell(v, m.format) + " |")
				if g.ratioAfter(m, i) {
					b.WriteString(" " + g.ratio(m, phases, i) + " |")
				}
			}
		}
		b.WriteString("\n")
	}
}

// ratioAfter reports whether run i's column of m is followed by its ratio to
// the baseline run.
func (g *gwRender) ratioAfter(m gwMetric, i int) bool {
	return m.ratio && g.baseline >= 0 && i != g.baseline
}

func (g *gwRender) ratio(m gwMetric, phases []*gwPhase, i int) string {
	if phases[i] == nil || phases[g.baseline] == nil {
		return "—"
	}
	v, base := m.value(phases[i]), m.value(phases[g.baseline])
	if !v.ok || !base.ok || base.v == 0 {
		return "—"
	}
	return strconv.FormatFloat(v.v/base.v, 'f', 2, 64)
}

func cell(v opt, format func(float64) string) string {
	if !v.ok {
		return "—"
	}
	return format(v.v)
}

// phase is the run's phase of the row k in table t, or nil.
func (r *gwRun) phase(t gwTable, k gwRowKey) *gwPhase {
	for i := range r.phases {
		ph := &r.phases[i]
		if slices.Contains(t.profiles, ph.profile) && ph.variant == k.variant && ph.threads == k.threads {
			return ph
		}
	}
	return nil
}

func (g *gwRender) renderGC(b *strings.Builder, t gwTable) {
	var lines []string
	torn := false
	for _, r := range g.runs {
		for _, p := range t.profiles {
			gc, ok := r.gc[p]
			if !ok {
				continue
			}
			line := fmt.Sprintf("- %s, %s: %d cycles, longest stop-the-world pause %s µs, %s µs in all",
				r.label, p, gc.cycles, num(gc.maxPauseUs), num(gc.totalPauseUs))
			switch {
			case gc.torn == 1:
				line += "; 1 torn line, its cycle's pauses unknown"
			case gc.torn > 1:
				line += fmt.Sprintf("; %d torn lines, their cycles' pauses unknown", gc.torn)
			}
			torn = torn || gc.torn > 0
			lines = append(lines, line)
		}
	}
	if len(lines) == 0 {
		return
	}
	b.WriteString("\nGC cycles over the whole profile, its preparation and removals included, from GODEBUG=gctrace=1; " +
		"each cycle stops the world twice")
	if torn {
		b.WriteString(". A torn line is a cycle's line cut short, by a record of rgw-go's JSON log, which shares its " +
			"stderr, or by the end of the log the run read; one torn too early to be kept is known from the gap it leaves " +
			"in the cycles' numbers. A torn line's cycle counts, and its pauses are in neither the longest nor the total")
	}
	b.WriteString(":\n\n")
	b.WriteString(strings.Join(lines, "\n") + "\n")
}

func (g *gwRender) renderProfiles(b *strings.Builder) {
	var lines []string
	for _, r := range g.runs {
		if len(r.profiles) > 0 {
			lines = append(lines, fmt.Sprintf("- %s: %s", r.label, strings.Join(r.profiles, ", ")))
		}
	}
	if len(lines) == 0 {
		return
	}
	b.WriteString("\n## CPU profiles\n\nEach profile covers its profile's last phase, taken from the gateway's " +
		"/debug/pprof/profile for the phase's time limit; read them with go tool pprof.\n\n")
	b.WriteString(strings.Join(lines, "\n") + "\n")
}

func (g *gwRender) renderConfig(b *strings.Builder) {
	type column struct {
		run    *gwRun
		entity string
	}
	var cols []column
	options := map[string]bool{}
	for _, r := range g.runs {
		for _, e := range slices.Sorted(maps.Keys(r.config)) {
			cols = append(cols, column{r, e})
			for o := range r.config[e].Options {
				options[o] = true
			}
		}
	}
	if len(cols) == 0 {
		return
	}
	b.WriteString("\n## Parity options\n\n| option |")
	for _, c := range cols {
		fmt.Fprintf(b, " %s: %s (%s) |", c.run.label, c.entity, c.run.config[c.entity].Source)
	}
	b.WriteString("\n|" + strings.Repeat("---|", len(cols)+1) + "\n")
	for _, o := range slices.Sorted(maps.Keys(options)) {
		fmt.Fprintf(b, "| %s |", o)
		for _, c := range cols {
			v, ok := c.run.config[c.entity].Options[o]
			if !ok {
				v = "—"
			}
			fmt.Fprintf(b, " %s |", v)
		}
		b.WriteString("\n")
	}
}

func (g *gwRender) renderEnv(b *strings.Builder) {
	keys := map[string]bool{}
	for _, r := range g.runs {
		for _, k := range r.envKeys {
			keys[k] = true
		}
	}
	b.WriteString("\n## Environment\n\n| env.json |")
	for _, r := range g.runs {
		fmt.Fprintf(b, " %s |", r.label)
	}
	b.WriteString("\n|" + strings.Repeat("---|", len(g.runs)+1) + "\n")
	for _, k := range slices.Sorted(maps.Keys(keys)) {
		fmt.Fprintf(b, "| %s |", k)
		for _, r := range g.runs {
			v, ok := r.env[k]
			s := "—"
			if ok {
				s = envValue(v)
			}
			fmt.Fprintf(b, " %s |", s)
		}
		b.WriteString("\n")
	}
}

func (g *gwRender) renderCommands(b *strings.Builder) {
	b.WriteString("\n## Commands\n\nEvery measured command, as elbencho and httpload recorded it; elbencho leaves out the secret key.\n")
	for _, r := range g.runs {
		fmt.Fprintf(b, "\n### %s\n\n```\n", r.label)
		for i := range r.phases {
			b.WriteString(strings.TrimSpace(r.phases[i].command) + "\n")
		}
		b.WriteString("```\n")
	}
}
