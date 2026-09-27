package radosclient

// Step is one entry of an op, exported so implementations translate it and
// tests inspect it. Only this package's step types satisfy it, each as a
// pointer.
type Step interface{ isStep() }

// ExecStep calls a RADOS object-class method.
type ExecStep struct {
	Class, Method string
	In            []byte
	Result        *ExecResult
}

// ReadStep reads Length bytes at Offset. A Length of 0 asks RADOS for
// everything from Offset to the end of the object, into an empty buffer: it
// succeeds with no data when nothing lies past Offset and fails the step with
// ERANGE otherwise, so read a known size.
type ReadStep struct {
	Offset, Length uint64
	Result         *ReadResult
}

// StatStep reads the object's size and modification time.
type StatStep struct {
	Result *StatResult
}

// GetXattrsStep reads every extended attribute.
type GetXattrsStep struct {
	Result *XattrsResult
}

// CmpXattrStep fails the op unless the named xattr compares to Value under Op.
type CmpXattrStep struct {
	Name  string
	Op    CmpOp
	Value []byte
}

// AssertExistsStep fails the op with ENOENT unless the object exists.
type AssertExistsStep struct{}

// AssertVersionStep fails the op unless the object is at Version.
type AssertVersionStep struct {
	Version uint64
}

// OmapGetValsStep reads up to Max omap entries after StartAfter whose keys begin with FilterPrefix.
type OmapGetValsStep struct {
	StartAfter, FilterPrefix string
	Max                      uint64
	Result                   *OmapResult
}

// OmapGetValsByKeysStep reads the omap entries for Keys.
type OmapGetValsByKeysStep struct {
	Keys   []string
	Result *OmapResult
}

// OmapGetKeysStep reads up to Max omap keys after StartAfter.
type OmapGetKeysStep struct {
	StartAfter string
	Max        uint64
	Result     *OmapKeysResult
}

// CreateStep creates the object, failing with EEXIST if Exclusive and it exists.
type CreateStep struct {
	Exclusive bool
}

// RemoveStep deletes the object.
type RemoveStep struct{}

// StepFlagsStep sets the flags of the step before it.
type StepFlagsStep struct {
	Flags StepFlags
}

// WriteFullStep replaces the object's data with Data.
type WriteFullStep struct {
	Data []byte
}

// WriteStep writes Data at Offset.
type WriteStep struct {
	Data   []byte
	Offset uint64
}

// AppendStep appends Data to the object.
type AppendStep struct {
	Data []byte
}

// ZeroStep zeroes Length bytes at Offset.
type ZeroStep struct {
	Offset, Length uint64
}

// TruncateStep truncates the object to Offset bytes.
type TruncateStep struct {
	Offset uint64
}

// SetXattrStep sets the named extended attribute to Value.
type SetXattrStep struct {
	Name  string
	Value []byte
}

// RmXattrStep removes the named extended attribute.
type RmXattrStep struct {
	Name string
}

// OmapSetStep sets the given omap entries.
type OmapSetStep struct {
	Values map[string][]byte
}

// OmapRmKeysStep removes the given omap keys.
type OmapRmKeysStep struct {
	Keys []string
}

// OmapClearStep removes every omap entry.
type OmapClearStep struct{}

// OmapCmpStep fails the op unless the omap value at Key compares to Value under Op.
type OmapCmpStep struct {
	Key   string
	Op    CmpOp
	Value []byte
}

// SetAllocHintStep advises the OSD of the object's expected size and write size.
type SetAllocHintStep struct {
	ExpectedObjectSize, ExpectedWriteSize uint64
	Flags                                 AllocHintFlags
}

func (*ExecStep) isStep()              {}
func (*ReadStep) isStep()              {}
func (*StatStep) isStep()              {}
func (*GetXattrsStep) isStep()         {}
func (*CmpXattrStep) isStep()          {}
func (*AssertExistsStep) isStep()      {}
func (*AssertVersionStep) isStep()     {}
func (*OmapGetValsStep) isStep()       {}
func (*OmapGetValsByKeysStep) isStep() {}
func (*OmapGetKeysStep) isStep()       {}
func (*CreateStep) isStep()            {}
func (*RemoveStep) isStep()            {}
func (*StepFlagsStep) isStep()         {}
func (*WriteFullStep) isStep()         {}
func (*WriteStep) isStep()             {}
func (*AppendStep) isStep()            {}
func (*ZeroStep) isStep()              {}
func (*TruncateStep) isStep()          {}
func (*SetXattrStep) isStep()          {}
func (*RmXattrStep) isStep()           {}
func (*OmapSetStep) isStep()           {}
func (*OmapRmKeysStep) isStep()        {}
func (*OmapClearStep) isStep()         {}
func (*OmapCmpStep) isStep()           {}
func (*SetAllocHintStep) isStep()      {}
