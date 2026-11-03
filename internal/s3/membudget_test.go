package s3_test

import (
	"context"
	"fmt"
	"io"
	"net/http"

	. "github.com/onsi/ginkgo/v2"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
)

// errNoRoom is the RADOS driver's refusal of a request whose first draw on
// the memory budget waited out its deadline.
var errNoRoom = fmt.Errorf("%w: waited 5s for 4194304 bytes of the memory budget's piece room", op.ErrSlowDown)

// noRoomObjects is an object store whose reads and writes find no room in
// the memory budget, before any byte of their data goes out or comes in.
type noRoomObjects struct{ op.ObjectStore }

func (noRoomObjects) ReadObject(context.Context, *op.ObjectState, op.ByteRange, io.Writer) error {
	return errNoRoom
}

func (noRoomObjects) PutObject(context.Context, *op.BucketRecord, meta.ObjKey, io.Reader, op.PutParams) (*op.PutResult, error) {
	return nil, errNoRoom
}

var _ = Describe("a request the memory budget refuses", func() {
	var w *writeWorld
	BeforeEach(func(ctx SpecContext) {
		w = newWriteWorld(ctx, denc.Squid)
		w.env.Objects = noRoomObjects{ObjectStore: w.store}
	})

	It("answers a GET 503 SlowDown with radosgw's error document", func() {
		expectError(w.send(w.alice, http.MethodGet, "/plain/src", ""), http.StatusServiceUnavailable, "SlowDown")
	})

	It("answers a PUT 503 SlowDown with radosgw's error document", func() {
		expectError(w.send(w.alice, http.MethodPut, "/plain/k", "hello"), http.StatusServiceUnavailable, "SlowDown")
	})
})
