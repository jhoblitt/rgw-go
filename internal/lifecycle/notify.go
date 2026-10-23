package lifecycle

import (
	"context"

	"github.com/jhoblitt/rgw-go/internal/meta"
)

// Event is a bucket notification event type the lifecycle worker publishes,
// named as rgw::notify::to_string names it
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_notify_event_type.cc#L31-L56 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_notify_event_type.cc#L31-L56 at v20.2.4).
type Event string

// The events of the actions the worker takes on a bucket never versioned.
const (
	EventExpirationCurrent  Event = "s3:ObjectLifecycle:Expiration:Current"
	EventExpirationDelete   Event = "s3:LifecycleExpiration:Delete"
	EventExpirationAbortMPU Event = "s3:ObjectLifecycle:Expiration:AbortMPU"
)

// Notification is what send_notification publishes for one action
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L550-L577 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L557-L584 at v20.2.4):
// the bucket and the object acted on, its ETag and size as the action read
// them, the version the listing named, and the event types.
type Notification struct {
	Bucket    meta.BucketID
	Key       meta.ObjKey
	ETag      string
	Size      uint64
	VersionID string
	Events    []Event
}

//counterfeiter:generate . Notifier

// Notifier publishes the notification of an action the worker took, where
// radosgw's worker calls send_notification. radosgw logs a failed
// reservation or commit and goes on, so Notify returns nothing.
type Notifier interface {
	Notify(ctx context.Context, n Notification)
}

// NopNotifier publishes nothing, as a gateway that serves no bucket
// notification configuration has nothing to publish to.
type NopNotifier struct{}

// Notify implements Notifier.
func (NopNotifier) Notify(context.Context, Notification) {}
