package policy

// ActionSet is rgw::IAM::Action_t: one bit per Action.
type ActionSet [(ActionCount + 63) / 64]uint64 //nolint:recvcheck // Set and Union change the set; the queries take a value so AllValue().Has(a) needs no variable

// Set adds a to s. A value at or past ActionCount names no action and is
// ignored.
func (s *ActionSet) Set(a Action) {
	if a < ActionCount {
		s[a/64] |= 1 << (a % 64)
	}
}

// Has reports whether a is in s; it is false for a value at or past
// ActionCount.
func (s ActionSet) Has(a Action) bool {
	return a < ActionCount && s[a/64]&(1<<(a%64)) != 0
}

// IsZero reports whether s holds no action.
func (s ActionSet) IsZero() bool {
	return s == ActionSet{}
}

// Equal reports whether s and o hold the same actions.
func (s ActionSet) Equal(o ActionSet) bool {
	return s == o
}

// Contains reports whether every action in o is in s.
func (s ActionSet) Contains(o ActionSet) bool {
	for i := range s {
		if o[i]&^s[i] != 0 {
			return false
		}
	}
	return true
}

// Union adds every action in o to s.
func (s *ActionSet) Union(o ActionSet) {
	for i := range s {
		s[i] |= o[i]
	}
}

// contBits is set_cont_bits (src/rgw/rgw_iam_policy.h:219-223 at v19.2.6):
// the actions from start up to, not including, end.
func contBits(start, end Action) ActionSet {
	var s ActionSet
	for a := start; a < end; a++ {
		s.Set(a)
	}
	return s
}

// The per-service values of src/rgw/rgw_iam_policy.h:226-232 at v19.2.6
// (:236-242 at v20.2.4). Each covers its service's block without the
// service's All; allValue covers every action.
var (
	s3AllValue             = contBits(0, S3All)
	s3ObjectLambdaAllValue = contBits(S3All+1, S3ObjectLambdaAll)
	iamAllValue            = contBits(S3ObjectLambdaAll+1, IAMAll)
	stsAllValue            = contBits(IAMAll+1, STSAll)
	snsAllValue            = contBits(STSAll+1, SNSAll)
	organizationsAllValue  = contBits(SNSAll+1, OrganizationsAll)
	allValue               = contBits(0, ActionCount)
)

// S3AllValue is s3AllValue: every s3 action, without S3All.
func S3AllValue() ActionSet { return s3AllValue }

// S3ObjectLambdaAllValue is s3objectlambdaAllValue: every s3-object-lambda
// action, without S3ObjectLambdaAll.
func S3ObjectLambdaAllValue() ActionSet { return s3ObjectLambdaAllValue }

// IAMAllValue is iamAllValue: every iam action, without IAMAll.
func IAMAllValue() ActionSet { return iamAllValue }

// STSAllValue is stsAllValue: every sts action, without STSAll.
func STSAllValue() ActionSet { return stsAllValue }

// SNSAllValue is snsAllValue: every sns action, without SNSAll.
func SNSAllValue() ActionSet { return snsAllValue }

// OrganizationsAllValue is organizationsAllValue: every organizations action,
// without OrganizationsAll.
func OrganizationsAllValue() ActionSet { return organizationsAllValue }

// AllValue is allValue, what radosgw sets for an Action of "*": every action,
// the service All wildcards included.
func AllValue() ActionSet { return allValue }
