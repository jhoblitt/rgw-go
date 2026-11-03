package memlimit

// SetProcess makes ForProcess read s's cgroup and environment, and set its
// limit, in place of the running process's until the returned restore runs.
func SetProcess(s Sizer) (restore func()) {
	oldCgroup, oldEnv, oldLimit := processCgroup, processEnv, processLimit
	processCgroup, processEnv, processLimit = s.Cgroup, s.LookupEnv, s.SetLimit
	return func() { processCgroup, processEnv, processLimit = oldCgroup, oldEnv, oldLimit }
}
