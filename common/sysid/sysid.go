package sysid

type (
	// A sysid.Id represents a "system" that a package can be built for, used for the narrow
	// purpose of excluding sources for differential compression. Sources are only considered
	// if they have the same sysid.Id. It's similar to a hash code in that extra collisions are
	// merely a performance problem, not a correctness one. But also, differences when they
	// should be equal will also cause suboptimial performance.
	//
	// Ids are stored in the database and network so should be stable.
	//
	// Format (low bits to high):
	//   2: version
	//   8: arch
	//   8: libc
	//   rest: reserved
	Id uint32
)

const (
	Version_0 = 0

	// Arch is the platform architecture
	Arch_unknown = 0
	Arch_x86_64  = 1
	Arch_i686    = 2
	Arch_aarch64 = 3

	// Libc is the libc or other runtime
	Libc_unknown = 0
	Libc_glibc   = 1
	Libc_musl    = 2
)
