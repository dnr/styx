package sysid

type (
	// A sysid.Id represents a "system" that a package can be built for, used for the narrow
	// purpose of excluding sources for differential compression. Sources are only considered
	// if they have the same sysid.Id. It's similar to a hash code in that extra collisions are
	// merely a performance problem, not a correctness one. But also, differences when they
	// should be equal will also cause suboptimial performance.
	//
	// Ids are stored in the database so should be stable.
	//
	// Format (low bits to high):
	//   2: version
	//   8: arch
	//   8: libc
	//   rest: reserved
	Id uint32
)

const (
	Version_0 Id = 0 << 0

	// Arch is the platform architecture
	Arch_unknown Id = 0 << 2
	Arch_x86_64  Id = 1 << 2
	Arch_i686    Id = 2 << 2
	Arch_aarch64 Id = 3 << 2

	// Libc is the libc or other runtime
	Libc_unknown Id = 0 << 10
	Libc_glibc   Id = 1 << 10
	Libc_musl    Id = 2 << 10

	// Manifests can't mix with data anyway so just use zero
	Manifest Id = Version_0
	// Anything unknown can use 0 also
	Unknown Id = Version_0
)
