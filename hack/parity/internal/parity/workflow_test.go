package parity_test

import (
	"os"
	"regexp"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"go.yaml.in/yaml/v3"
)

// workflowStep is a step of a GitHub Actions job, as far as the specs read
// one.
type workflowStep struct {
	Name string            `yaml:"name"`
	Uses string            `yaml:"uses"`
	With map[string]any    `yaml:"with"`
	Env  map[string]string `yaml:"env"`
	Run  string            `yaml:"run"`
}

// workflow is a GitHub Actions workflow, as far as the specs read one.
type workflow struct {
	On          map[string]map[string]any `yaml:"on"`
	Permissions map[string]string         `yaml:"permissions"`
	Jobs        map[string]struct {
		Permissions any            `yaml:"permissions"`
		Steps       []workflowStep `yaml:"steps"`
	} `yaml:"jobs"`
}

const workflowDir = "../../../../.github/workflows/"

// readWorkflow returns the workflow file name holds, and its text.
func readWorkflow(name string) (workflow, string) {
	data, err := os.ReadFile(workflowDir + name)
	Expect(err).NotTo(HaveOccurred())
	var wf workflow
	Expect(yaml.Unmarshal(data, &wf)).To(Succeed())
	return wf, string(data)
}

// stepIndex returns the index of the one step of steps that match
// reports, failing the spec unless exactly one does.
func stepIndex(steps []workflowStep, what string, match func(workflowStep) bool) int {
	var found []int
	for i, s := range steps {
		if match(s) {
			found = append(found, i)
		}
	}
	Expect(found).To(HaveLen(1), "steps that %s", what)
	return found[0]
}

var _ = Describe("the s3tests workflow", func() {
	It("points every comparison's summary at the job summary, so that a dropped test shows on the pull request",
		func() {
			wf, _ := readWorkflow("s3tests.yml")
			steps := wf.Jobs["parity"].Steps
			compare := steps[stepIndex(steps, "run make s3tests-parity", func(s workflowStep) bool {
				return strings.Contains(s.Run, "make s3tests-parity")
			})]
			var calls []string
			for line := range strings.SplitSeq(compare.Run, "\n") {
				if strings.Contains(line, "make s3tests-parity") {
					calls = append(calls, line)
				}
			}
			Expect(calls).To(HaveLen(2))
			for _, call := range calls {
				Expect(call).To(ContainSubstring(`"SUMMARY=${GITHUB_STEP_SUMMARY}"`))
			}
		})
})

var _ = Describe("the ledgers workflow", func() {
	var (
		wf    workflow
		text  string
		steps []workflowStep
		// build, fetch and check index the steps that build the checker,
		// fetch the pull request's head and run the check.
		build, fetch, check int
	)

	BeforeEach(func() {
		wf, text = readWorkflow("ledgers.yml")
		Expect(wf.Jobs).To(HaveLen(1))
		Expect(wf.Jobs).To(HaveKey("ledgers"))
		steps = wf.Jobs["ledgers"].Steps
		build = stepIndex(steps, "build with go", func(s workflowStep) bool { return strings.Contains(s.Run, "go build") })
		fetch = stepIndex(steps, "fetch", func(s workflowStep) bool { return strings.Contains(s.Run, "git fetch") })
		check = stepIndex(steps, "run the check", func(s workflowStep) bool { return strings.Contains(s.Run, "parity\" check") })
	})

	It("runs on pull_request_target alone, so that GitHub runs the default branch's copy, on every pull request", func() {
		Expect(wf.On).To(HaveLen(1))
		Expect(wf.On).To(HaveKey("pull_request_target"))
		trigger := wf.On["pull_request_target"]
		for _, filter := range []string{"paths", "paths-ignore", "branches", "branches-ignore"} {
			Expect(trigger).NotTo(HaveKey(filter))
		}
		Expect(trigger["types"]).To(ContainElements("opened", "synchronize", "reopened"))
	})

	It("is the only home of the job: ci.yml, which runs the pull request's copy, holds none", func() {
		ci, _ := readWorkflow("ci.yml")
		Expect(ci.Jobs).NotTo(HaveKey("ledgers"))
	})

	It("reads contents and nothing more, keeps no token and uses no secret", func() {
		Expect(wf.Permissions).To(Equal(map[string]string{"contents": "read"}))
		Expect(wf.Jobs["ledgers"].Permissions).To(BeNil())
		Expect(text).NotTo(ContainSubstring("secrets."))
		checkout := steps[stepIndex(steps, "use actions/checkout", func(s workflowStep) bool {
			return strings.HasPrefix(s.Uses, "actions/checkout@")
		})]
		Expect(checkout.With).To(HaveKeyWithValue("persist-credentials", false))
	})

	It("checks out the default branch, never the pull request", func() {
		checkout := steps[stepIndex(steps, "use actions/checkout", func(s workflowStep) bool {
			return strings.HasPrefix(s.Uses, "actions/checkout@")
		})]
		Expect(checkout.With).NotTo(HaveKey("ref"))
		Expect(checkout.With).NotTo(HaveKey("repository"))
		for _, s := range steps {
			for _, verb := range []string{"checkout", "switch", "worktree", "reset", "restore", "read-tree", "archive", "source ", "bash ", "make "} {
				Expect(s.Run).NotTo(ContainSubstring(verb), s.Name)
			}
		}
		for _, ref := range []string{"head.ref", "head.repo", "head_ref", "merge_commit_sha", "refs/pull"} {
			Expect(text).NotTo(ContainSubstring(ref))
		}
	})

	It("puts no expression into a shell, passing the event's ids through the environment", func() {
		for _, s := range steps {
			Expect(s.Run).NotTo(ContainSubstring("${{"), s.Name)
		}
		Expect(steps[fetch].Env).To(Equal(map[string]string{
			"HEAD_SHA": "${{ github.event.pull_request.head.sha }}",
			"BASE_SHA": "${{ github.event.pull_request.base.sha }}",
		}))
	})

	It("checks that the head and base are full commit ids before git sees them", func() {
		run := steps[fetch].Run
		valid := strings.Index(run, `re='^[0-9a-f]{40}$'`)
		Expect(valid).To(BeNumerically(">=", 0))
		Expect(run).To(ContainSubstring(`[[ ! "$HEAD_SHA" =~ $re || ! "$BASE_SHA" =~ $re ]]`))
		Expect(valid).To(BeNumerically("<", strings.Index(run, "git ")))
		Expect(run).To(HavePrefix("set -euo pipefail\n"))
	})

	It("builds the checker before the head is fetched, with no cache restored and empty caches of its own", func() {
		Expect(build).To(BeNumerically("<", fetch))
		Expect(fetch).To(BeNumerically("<", check))
		setup := steps[stepIndex(steps, "use actions/setup-go", func(s workflowStep) bool {
			return strings.HasPrefix(s.Uses, "actions/setup-go@")
		})]
		Expect(setup.With).To(HaveKeyWithValue("cache", false))
		for _, s := range steps {
			Expect(s.Uses).NotTo(ContainSubstring("cache"), s.Name)
		}
		run := steps[build].Run
		at := strings.Index(run, "go build")
		for _, v := range []string{"GOMODCACHE", "GOCACHE"} {
			set := strings.Index(run, v+`=$(mktemp -d "$RUNNER_TEMP/`)
			Expect(set).To(BeNumerically(">=", 0), v+" is not set to a fresh directory")
			Expect(set).To(BeNumerically("<", at), v+" is set after the build")
		}
		Expect(run).To(ContainSubstring("export GOMODCACHE GOCACHE"))
		Expect(run).NotTo(ContainSubstring("-C"))
	})

	It("judges the head's ledgers as data against its merge base", func() {
		Expect(steps[check].Run).To(Equal(`"$RUNNER_TEMP/parity" check --base "$MERGE_BASE" --head "$HEAD_SHA" --summary "$GITHUB_STEP_SUMMARY"`))
		Expect(steps[fetch].Run).To(ContainSubstring(`base=$(git merge-base --end-of-options "$BASE_SHA" "$HEAD_SHA")`))
	})

	It("pins every action to a commit", func() {
		pinned := regexp.MustCompile(`^[\w.-]+/[\w./-]+@[0-9a-f]{40}$`)
		actions := 0
		for _, s := range steps {
			if s.Uses != "" {
				actions++
				Expect(pinned.MatchString(s.Uses)).To(BeTrue(), s.Uses)
			}
		}
		Expect(actions).To(Equal(2))
	})
})
