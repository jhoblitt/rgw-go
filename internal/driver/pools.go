package driver

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// poolCache hands out one radosclient.Pool per pool and namespace for the
// life of the Store; closeAll releases them all.
type poolCache struct {
	mu      sync.Mutex
	cluster radosclient.Cluster
	pools   map[meta.Pool]radosclient.Pool
}

func newPoolCache(c radosclient.Cluster) *poolCache {
	return &poolCache{cluster: c, pools: map[meta.Pool]radosclient.Pool{}}
}

// get returns the handle on p, opening it on first use. The driver never
// creates a pool, so a missing one is an error naming it.
func (c *poolCache) get(ctx context.Context, p meta.Pool) (radosclient.Pool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if pool, ok := c.pools[p]; ok {
		return pool, nil
	}
	pool, err := c.cluster.Pool(ctx, p.Name, p.NS)
	if err != nil {
		return nil, fmt.Errorf("opening pool %s: %w", p, err)
	}
	c.pools[p] = pool
	return pool, nil
}

// closeAll closes every handle the cache opened.
func (c *poolCache) closeAll() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	var errs []error
	for k, p := range c.pools {
		errs = append(errs, p.Close())
		delete(c.pools, k)
	}
	return errors.Join(errs...)
}
