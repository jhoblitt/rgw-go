#!/usr/bin/env python3
"""mkpatch.py: writes stat.go.nocopy from stat.go.orig (296ad44's internal/driver/stat.go): the
THROWAWAY variant FNC, in which a GET's prefetched head is served from its free-list buffer instead
of a copy. The buffer stays out of the list until the request is done with the state: when its
context ends (context.AfterFunc, as prefetchRelease does for the budget share) or the driver
discards the state (HeadRelease), whichever comes first, once. Exits non-zero if an anchor is
missing, so the patch never applies to code it was not written against."""
import sys

src = open(sys.argv[1]).read()


def sub(old, new):
    global src
    if src.count(old) != 1:
        sys.exit(f"anchor found {src.count(old)} times: {old[:60]!r}")
    src = src.replace(old, new)


sub('\t"slices"\n', '')
sub('\t"strings"\n', '\t"strings"\n\t"sync"\n')
sub('''		} else {
			defer s.bufs.put(buf)
		}
	}''', '''		} else {
			// THROWAWAY (realworld spike, variant FNC): a buffer the state keeps as its Head
			// goes back only when the request is done with it, below.
			defer func() {
				if !headKept {
					s.bufs.put(buf)
				}
			}()
		}
	}''')
sub('''	ver, err := ref.pool.Read(ctx, ref.oid, rop, radosclient.OpFlagNone)
	settled = true''', '''	ver, err := ref.pool.Read(ctx, ref.oid, rop, radosclient.OpFlagNone)
	settled = true
	headKept := false''')
sub('''		st.Head = slices.Clone(rd.Data[:rd.N])
		kept := min(share, int64(len(st.Head)))
		st.HeadRelease = s.mem.prefetchRelease(ctx, kept)
		share -= kept''', '''		// THROWAWAY (realworld spike, variant FNC): no copy. The free-list buffer is the
		// head; it returns to the list once, when ctx ends or the state is discarded.
		st.Head = rd.Data[:rd.N]
		kept := min(share, int64(len(st.Head)))
		budget := s.mem.prefetchRelease(ctx, kept)
		share -= kept
		headKept = true
		giveBack := sync.OnceFunc(func() { s.bufs.put(buf) })
		stop := context.AfterFunc(ctx, giveBack)
		st.HeadRelease = func() {
			stop()
			if budget != nil {
				budget()
			}
			giveBack()
		}''')
open(sys.argv[2], 'w').write(src)
