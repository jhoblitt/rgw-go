#!/usr/bin/env python3
"""mkfu.py: turns the copies gw-start-fu.sh and run-fu.sh into the follow-up harness: the variants
F1G (F's binary in a 1 GiB scope), FG50 (F's binary, GOGC=50) and FNC (the throwaway no-copy
build), and for FNC a content check (rwtool verify) of the run's bucket after sampling stops and
before its objects are removed. Every edit is anchored and must match exactly once."""
import sys

d = '/tmp/claude-1000/spike-memory/realworld/'


def edit(path, pairs):
    s = open(path).read()
    for old, new in pairs:
        if s.count(old) != 1:
            sys.exit(f"{path}: anchor found {s.count(old)} times: {old[:70]!r}")
        s = s.replace(old, new)
    open(path, 'w').write(s)


edit(d + 'gw-start-fu.sh', [
    ('# gw-start.sh VARIANT RUNDIR:', '# gw-start-fu.sh VARIANT RUNDIR: gw-start.sh for the follow-up variants too (F1G: F in a 1 GiB\n'
     '# scope; FG50: F with GOGC=50; FNC: the throwaway no-copy build).\n# gw-start.sh VARIANT RUNDIR:'),
    ('bin=$(variant_bin "$v")\n',
     'case $v in\n'
     'F1G | FG50) bin=$(variant_bin F) ;;\n'
     'FNC) bin=/home/jhoblitt/.cache/rgw-go-realworld/FNC/rgw-go ;;\n'
     '*) bin=$(variant_bin "$v") ;;\n'
     'esac\n'
     'vmax=$memmax\n'
     '[ "$v" = F1G ] && vmax=1073741824\n'
     'venv=()\n'
     '[ "$v" = FG50 ] && venv=(GOGC=50)\n'),
    ('	RGW_GO_LOG_FORMAT=json GODEBUG=gctrace=1 \\\n', '	RGW_GO_LOG_FORMAT=json GODEBUG=gctrace=1 "${venv[@]}" \\\n'),
    ('-p MemoryMax="$memmax"', '-p MemoryMax="$vmax"'),
])

edit(d + 'run-fu.sh', [
    ('"$here/gw-start.sh" "$v" "$rd"\n', '"$here/gw-start-fu.sh" "$v" "$rd"\n'),
    ('"$here/snap.sh" "$v" "$pid" "$rd/snaps/end" full\n',
     '"$here/snap.sh" "$v" "$pid" "$rd/snaps/end" full\n'
     '# The throwaway FNC serves heads from free-list buffers: every object is read back and checked\n'
     '# against its ETag, at the load\'s own concurrency, before the objects go.\n'
     'if [ "$v" = FNC ]; then\n'
     '	case $load in\n'
     '	large) "$here/bin/rwtool-fu" verify -endpoint "$endpoint" -bucket rw-large -threads 32 -passes 4 >"$rd/verify.json" 2>&1 || true ;;\n'
     '	small) "$here/bin/rwtool-fu" verify -endpoint "$endpoint" -bucket rw-small -threads 64 -passes 4 >"$rd/verify.json" 2>&1 || true ;;\n'
     '	esac\n'
     'fi\n'),
])
