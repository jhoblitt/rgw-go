package cephconf

import (
	"fmt"
	"slices"
	"strings"
)

// EarlyArgs is what ceph_argparse_early_args consumes before any config is
// read (src/common/ceph_argparse.cc): the arguments that decide which
// cluster, entity name and config file the rest is parsed against.
type EarlyArgs struct {
	// Cluster is --cluster, "" when it is absent; the caller then derives
	// radosgw's name as md_config_t::parse_config_files does at v19.2.6 and
	// v20.2.4. Without -c it is "ceph", with or without --no-config-file.
	// With -c it is the base name, less ".conf", of the file read from the
	// list, or "ceph" when that name does not end in ".conf"; when no file in
	// the list can be read, which radosgw tolerates only under
	// --no-config-file, it stays "".
	Cluster string
	// Name is the entity name TYPE.ID. -n/--name sets both ("client.rgw.a");
	// -i/--id/--user set the ID and keep the type, so "rgw.a" becomes
	// "client.rgw.a"; default "client.admin".
	Name string
	// ConfFile is -c/--conf, "" for the default search path.
	ConfFile string
	// NoConfigFile is --no-config-file. ceph then skips only the default
	// search path: it still reads a ConfFile given with it, or else the files
	// CEPH_CONF names, and tolerates finding none of them.
	NoConfigFile bool
	// Version is -v/--version: print the version and exit before connecting.
	Version bool
	// Rest is every other argument, in order, for librados to parse.
	Rest []string
}

// entityTypes are the types EntityName::from_str accepts
// (src/common/entity_name.cc at v19.2.6 and v20.2.4).
var entityTypes = []string{"auth", "mon", "osd", "mds", "mgr", "client"}

// ParseEarly consumes the early arguments from argv. A "--" ends the scan
// and stays in Rest. A -n/--name whose value is not TYPE.ID is an error, as
// is an option missing its value. -v/--version ends the scan, where ceph
// prints its version and exits. --show_args is dropped.
//
// ceph takes -i only for daemons other than clients, and radosgw starts as
// a client; rgw-go takes it as --id.
func ParseEarly(argv []string) (EarlyArgs, error) {
	var e EarlyArgs
	typ, id := "client", "admin"
scan:
	for i := 0; i < len(argv); i++ {
		arg := argv[i]
		switch {
		case arg == "--":
			e.Rest = append(e.Rest, argv[i:]...)
			break scan
		case isFlag(arg, "--version", "-v"):
			e.Version = true
			break scan
		case isFlag(arg, "--no-config-file"):
			e.NoConfigFile = true
			continue
		case isFlag(arg, "--show_args"):
			continue
		}
		opt, val, inline := withArg(arg, "--conf", "-c", "--cluster", "-i", "--id", "--user", "--name", "-n")
		if opt == "" {
			e.Rest = append(e.Rest, arg)
			continue
		}
		if !inline {
			if i+1 == len(argv) {
				return EarlyArgs{}, fmt.Errorf("option %s requires an argument", arg)
			}
			i++
			val = argv[i]
		}
		switch opt {
		case "--conf", "-c":
			e.ConfFile = val
		case "--cluster":
			e.Cluster = val
		case "-i", "--id", "--user":
			id = val
		case "--name", "-n":
			t, n, ok := strings.Cut(val, ".")
			if !ok || !slices.Contains(entityTypes, t) {
				return EarlyArgs{}, fmt.Errorf("parsing name %q: expected string of the form TYPE.ID, valid types are: %s",
					val, strings.Join(entityTypes, ", "))
			}
			typ, id = t, n
		}
	}
	e.Name = typ + "." + id
	return e, nil
}

// argKey is ceph's dashes_to_underscores: past the first two characters
// every dash up to the first "=" becomes an underscore, so --no-config-file
// and --no_config_file are one option.
func argKey(s string) string {
	if len(s) <= 2 {
		return s
	}
	b := []byte(s)
	for i := 2; i < len(b) && b[i] != '='; i++ {
		if b[i] == '-' {
			b[i] = '_'
		}
	}
	return string(b)
}

// isFlag reports whether arg is one of the flags, which ceph matches whole.
func isFlag(arg string, flags ...string) bool {
	k := argKey(arg)
	return slices.ContainsFunc(flags, func(f string) bool { return argKey(f) == k })
}

// withArg matches arg against options taking a value, as "--opt=value" or
// as "--opt" with the value in the next argument; an argument that merely
// begins with an option's name, as --cluster-network does, matches nothing.
// It returns the option matched, "" for none, and the value when inline.
func withArg(arg string, opts ...string) (opt, val string, inline bool) {
	k := argKey(arg)
	for _, o := range opts {
		if rest, ok := strings.CutPrefix(k, argKey(o)); ok {
			if rest == "" {
				return o, "", false
			}
			if rest[0] == '=' {
				return o, rest[1:], true
			}
		}
	}
	return "", "", false
}
