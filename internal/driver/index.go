package driver

import (
	"context"
	"fmt"

	"golang.org/x/sync/errgroup"

	rgwcls "github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// The bucket index is RGWSI_BucketIndex_RADOS (services/svc_bi_rados.cc). A
// bare line number is v19.2.6's.

// indexPool is open_bucket_index_pool (:43-71; v20.2.4 :51-79): the bucket's
// explicit index pool when it has one, else the index pool of the zone
// placement its rule names. Only a rule with neither a name nor a storage
// class takes the zonegroup default placement's name
// (rgw_placement_rule::empty, rgw_placement_types.h:29-31 at both tags). A
// rule with a class and no name is looked up by its empty name, as radosgw
// looks it up. No storage class plays a part, the rule's or the default's,
// so a bucket whose class the zone lacks still opens its index. A
// placement the zone lacks is radosgw's -EINVAL. The lookup reads the map
// Placement reads, without Placement's default for an empty name, which
// takes the default's class too. radosgw creates an index pool it finds
// missing; rgw-go fails naming it (docs/exclusions.md, "A missing index
// pool is not created").
func (s *Store) indexPool(ctx context.Context, info *meta.BucketInfo) (radosclient.Pool, error) {
	if p := info.Bucket.ExplicitPlacement.IndexPool; p.Name != "" {
		return s.pools.get(ctx, p)
	}
	rule := info.PlacementRule
	name := rule.Name
	if name == "" && rule.StorageClass == "" {
		name = s.zone.ZoneGroup.DefaultPlacement.Name
	}
	pi, ok := s.zone.Params.PlacementPools[name]
	if !ok {
		return nil, fmt.Errorf("%w: the index of bucket %s: no placement %q in the zone", op.ErrInvalidArgument, info.Bucket.Name, name)
	}
	return s.pools.get(ctx, pi.IndexPool)
}

// shardOIDs is get_bucket_index_objects (:130-163; v20.2.4 :138-171) for
// the index generation gen: ".dir.<id>" alone for an unsharded index,
// ".dir.<id>.<shard>" for generation 0 and ".dir.<id>.<gen>.<shard>" after,
// in shard order.
func shardOIDs(info *meta.BucketInfo, gen meta.IndexLayoutGen) []string {
	n := gen.Layout.Normal.NumShards
	if n == 0 {
		return []string{info.IndexShardOID(gen, 0)}
	}
	oids := make([]string, n)
	for i := range n {
		oids[i] = info.IndexShardOID(gen, i)
	}
	return oids
}

// readShardHeaders is cls_bucket_head (:323-352) for the current index
// generation: one bucket_list of no entries per shard, at most
// rgw_bucket_index_max_aio in flight, the headers in shard order. A bucket
// without a bucket id is radosgw's -EIO (open_bucket_index_base, :83-86;
// v20.2.4 :91-94). The first shard read that fails stops further reads, as
// CLSRGWConcurrentIO stops issuing them, and its error, naming the shard,
// is the one returned. Tentacle's radosgw reads the omap headers directly
// instead, and answers -EIO where this answers a missing shard's ENOENT or
// a header-less shard's zero header (docs/exclusions.md, "Index shard
// headers are read as Squid reads them").
func (s *Store) readShardHeaders(ctx context.Context, info *meta.BucketInfo) ([]rgwcls.DirHeader, error) {
	pool, err := s.indexPool(ctx, info)
	if err != nil {
		return nil, err
	}
	if info.Bucket.ID == "" {
		return nil, fmt.Errorf("%w: bucket %s has no bucket id", op.ErrUnknown, info.Bucket.Name)
	}
	oids := shardOIDs(info, info.Layout.Current)
	headers := make([]rgwcls.DirHeader, len(oids))
	g, gctx := errgroup.WithContext(ctx)
	// readOptions floors the limit at 1; a Store not built by Open has
	// none, and a zero limit would block the first read for good.
	g.SetLimit(max(s.opts.bucketIndexMaxAIO, 1))
	for i, oid := range oids {
		g.Go(func() error {
			if err := gctx.Err(); err != nil {
				return err
			}
			rop := radosclient.NewReadOp()
			res := rgwcls.GetDirHeader(rop, s.release)
			if _, err := pool.Read(gctx, oid, rop, radosclient.OpFlagNone); err != nil {
				return fmt.Errorf("reading the header of index shard %s: %w", oid, err)
			}
			ret, err := res.Result()
			if err != nil {
				return fmt.Errorf("%w: the header of index shard %s: %w", op.ErrUnknown, oid, err)
			}
			headers[i] = ret.Dir.Header
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return headers, nil
}

// readIndexStats is read_stats (:395-427; v20.2.4 :560-592): the Main
// category's totals summed over the current generation's shards, as the
// bucket's entry with its placement rule. radosgw leaves the entry's
// creation time to its caller; this takes the bucket's.
func (s *Store) readIndexStats(ctx context.Context, info *meta.BucketInfo) (meta.BucketEnt, error) {
	headers, err := s.readShardHeaders(ctx, info)
	if err != nil {
		return meta.BucketEnt{}, err
	}
	ent := meta.BucketEnt{Bucket: info.Bucket, PlacementRule: info.PlacementRule, CreationTime: info.CreationTime}
	for _, h := range headers {
		if st, ok := h.Stats[rgwcls.CategoryMain]; ok {
			ent.Count += st.NumEntries
			ent.Size += st.TotalSize
			ent.SizeRounded += st.TotalSizeRounded
		}
	}
	return ent, nil
}
