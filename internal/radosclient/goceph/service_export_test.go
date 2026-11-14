package goceph

import (
	"sync"

	"github.com/ceph/go-ceph/rados"

	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// ClosedRegistrar is a Cluster with no connection whose Close has started, for
// specs of what its service-map calls refuse before they reach librados.
func ClosedRegistrar() radosclient.ServiceRegistrar { return &cluster{closed: true} }

// RegistrarCluster is a Cluster that also registers in the service map.
type RegistrarCluster interface {
	radosclient.Cluster
	radosclient.ServiceRegistrar
}

// UnconnectedCluster is a Cluster over a librados handle that never connects,
// for specs of what the service-map calls do before librados needs a cluster:
// it answers a refused registration and a status update itself, and keeps a
// first registration. Close shuts the handle down without sending anything.
func UnconnectedCluster() RegistrarCluster {
	conn, err := rados.NewConn()
	if err != nil {
		panic(err)
	}
	c := &cluster{conn: conn, mode: ModeSync, pools: map[*poolState]struct{}{}}
	c.drained = sync.NewCond(&c.mu)
	return c
}

// AioUsers is how many clusters hold the process-wide completion notifier.
func AioUsers() int {
	aioModes.mu.Lock()
	defer aioModes.mu.Unlock()
	return aioModes.users
}
