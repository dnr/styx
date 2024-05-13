

# Styx

## Introduction

Styx is an alternate binary substitution mechanism for Nix that provides
efficient disk and bandwidth usage, and on-demand fetching.

Before explaining what it does and how it works, let me motivate things a
little:

We all know that Nix uses a ton of storage and bandwidth. This is because of how
it works: each package contains absolute references to its dependencies, so if
package with dependents changes all the packages that depend on it have to
change also. If a package near the root of the dependency tree (e.g. glibc or
systemd) changes, pretty much everything changes.

When a package changes, you have to download a nar file containing that package.
The nar file is compressed with xz, which is as good as it gets for general
purpose compression, but it can only eliminate redundancy within the package,
not across packages or versions of a package.

When a package's dependency changes without any other change, the new version is
usually very similar to the old one. Small version bumps produce slightly bigger
changes but often still quite small. Nix's binary fetching and storage mechanism
can't take advantage of any of that, though, it always downloads a full
xz-compressed nar. If we could take advantage of the similarities of data across
versions of a package, we could save both bandwidth and storage space.

My first try at seeing if I could improve this situation was
[nix-sandwich][nxdch], which does differential compression for nar downloads.
This works surprisingly well! But it has a bunch of limitations: it struggles
with large packages, and it does nothing to improve the situation with local
disk storage. I wanted to use some of those ideas but also try to address the
storage problem at the same time.

### Storage

Nix's optimize-store works by hard-linking files. This provides some benefit,
but only if the whole file is identical. Files that are very similar, even the
same except for a few changed hashes, get no benefit at all.

To do better we have to take advantage of common data within files. Some modern
filesystems like btrfs provide a way to share extents (block-aligned ranges of
files). If we combined that with differential-compressed downloads, that would
address both parts of the problem.

### On-demand

But I was also thinking about another obvious way to reduce bandwidth and
storage: not downloading or storing files that aren't used. For various reasons,
it's common for packages to end up in NixOS system closure that aren't actually
used. It seems silly to download new versions of them at every system update.

I also have a bunch of packages in my configuration that I use rarely. For
example, I might fire up [darktable][dt] only a few times a year, but I end up
downloading it with every update. Sure, I could leave it out of my configuration
and just use `nix-shell -p`, but that's extra hassle I shouldn't have to do.

What if most packages on my system were "installed", but the data wasn't even
fetched until they were used? Of course, after it was fetched then it should be
cached locally for fast access.

### What about FUSE?

If you're familiar with the Linux filesystem space, you might be thinking FUSE:
run a daemon that serves a Nix store in a smart way, doing fancy [de]compression
and fetching data on-demand.

Well, Styx is similar to that, but better:

The problem with FUSE is the performance overhead. Since each stat and read (no
writes, packages in the store are immutable) has to go to the kernel, then to
userspace, then back to the kernel, there's unavoidable latency.

What we really want for this on-demand thing is a way for the kernel to ask us
for files or parts of files, and then store that data and serve it from kernel
space in the future.

### EROFS

It turns out there's some new experimental stuff in Linux that works exactly
like this: [EROFS][erofs] + [on-demand fscache][erofs_over_fscache] was created
for the [container ecosystem][nydus], but there's a lot of similarities with
what we want for Nix.

Styx hooks up this EROFS + fscache stuff to a Nix binary cache and local store,
plus an external service for on-demand differential compression, to get all the
properties we're looking for.

There's still some overhead compared to a plain filesystem, since we essentially
have a filesystem on a filesystem. However, once cached, stats and reads are
served with one trip into the kernel. (Benchmarks TBD)


## Overall design

TODO: image here

Pieces:

- Binary cache: Styx works with any existing Nix binary cache.
- Manifester/differ: This sits near the binary cache.
- Chunk store: Sits near the manifester/differ.
- Styx daemon: Runs on your machine.
- Nix daemon: Slightly modified Nix daemon to talk to Styx.





## Roadmap and future work

### Realistic

- Improve chunk diff selection algorithm. The current algorithm is a very crude
  heuristic. There's a lot of potential work here.
    - Improving base selection
        - Getting a 'system" for each package and only using same system as base
        - Using multiple bases
    - Better selection of base chunks (currently using offset in nar)
    - Better readahead heuristics (whole file at a time)
    - Exploring other approaches like simhash
    - Consider how to make diffs more cacheable
- Expanding compressed files
- Support for bare files
- GC: Ability to reclaim space after deleting packages
- CI to pre-create manifests for core packages after channel bumps
- Run a system with everything not needed by stage1+stage2 on styx
- Adaptive chunk sizes for less overhead on very large packages


### Slightly crazy

- Boot a system with almost everything in the store in styx (kernel + initrd
  copied to /boot, styx started in stage1)



[nxdch]: https://github.com/dnr/nix-sandwich/
[dt]: https://search.nixos.org/packages?show=darktable
[erofs]: https://erofs.docs.kernel.org/
[erofs_over_fscache]: https://git.kernel.org/pub/scm/linux/kernel/git/torvalds/linux.git/commit/?id=65965d9530b0c320759cd18a9a5975fb2e098462
[nydus]: https://nydus.dev/

