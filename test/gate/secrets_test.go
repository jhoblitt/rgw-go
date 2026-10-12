package gate_test

import (
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/format"

	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/test/gate"
)

// secret is a secret key an assertion compares as a plain string, which
// maskSecrets prints masked.
type secret string

// masked stands in for a secret key that is set.
const masked = "<redacted>"

// A failed assertion prints the values it compared into the job log, which is
// public, so every secret key Gomega would print is masked, at any depth: the
// populated users' and those in the user objects the gate decodes. An unset
// secret prints as empty, so a missing one still shows.
var _ = format.RegisterCustomFormatter(maskSecrets)

func maskSecrets(value any) (string, bool) {
	switch v := value.(type) {
	case secret:
		return fmt.Sprintf("%q", mask(string(v))), true
	case meta.AccessKey:
		v.Secret = mask(v.Secret)
		return fmt.Sprintf("%T%+v", v, v), true
	case gate.User:
		v.SecretKey = mask(v.SecretKey)
		return fmt.Sprintf("%T%+v", v, v), true
	}
	return "", false
}

func mask(s string) string {
	if s == "" {
		return ""
	}
	return masked
}

var _ = Describe("maskSecrets", func() {
	const (
		populated = "PopulatedUserSecret00000000000000000001"
		decoded   = "DecodedUserSecret0000000000000000000001"
	)

	It("keeps a compared secret out of Equal's failure message", func() {
		m := Equal(secret(populated))
		Expect(m.Match(secret(decoded))).To(BeFalse())
		msg := m.FailureMessage(secret(decoded))
		Expect(msg).To(ContainSubstring(masked))
		Expect(msg).NotTo(ContainSubstring(populated))
		Expect(msg).NotTo(ContainSubstring(decoded))
	})

	It("keeps a decoded key's secret out of HaveKey's failure message on the user's key map", func() {
		keys := map[string]meta.AccessKey{"AKEY": {ID: "AKEY", Secret: decoded, Active: true}}
		m := HaveKey("OTHER")
		Expect(m.Match(keys)).To(BeFalse())
		msg := m.FailureMessage(keys)
		Expect(msg).To(ContainSubstring("AKEY"))
		Expect(msg).NotTo(ContainSubstring(decoded))
	})

	It("masks a populated user nested in a slice", func() {
		out := format.Object([]gate.User{{UID: "alice", AccessKey: "AKEY", SecretKey: populated}}, 0)
		Expect(out).To(ContainSubstring("alice"))
		Expect(out).NotTo(ContainSubstring(populated))
	})

	It("prints an unset secret as empty", func() {
		Expect(format.Object(secret(""), 0)).To(ContainSubstring(`""`))
		Expect(format.Object(meta.AccessKey{ID: "AKEY"}, 0)).NotTo(ContainSubstring(masked))
	})
})
