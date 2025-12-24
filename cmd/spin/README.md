
# Spin — simple Nix pinning using Styx

## What is this?

It's a Nix pinning tool, like npins or flakes or many others.

Basically, I added "tarball" support to Styx for downloading nixpkgs, and by the
time I was done, I realized I had made half of a pinning tool. So I just made
the other half.

## What's different about it?

Not much really, the main thing is that it integrates with Styx and uses Styx to
fetch inputs, which comes with all the advantages of Styx for saving bandwidth
and disk space.

Most inputs are small so it doesn't make much difference. Nixpkgs is pretty big
and using Styx to download only deltas is nice.

## Should I use it?

Probably not, I made it for myself and it's pretty bare-bones.

## How do I use it?

First, set up Styx and set `services.styx.includeSpin = true`.
(Eventually I'll add a fallback so Styx isn't required.)

Then use commands:

```sh
spin add nixpkgs https://channels.nixos.org/nixos-25.11/nixexprs.tar.xz
spin add other https://github.com/owner/repo/archive/branch.tar.gz
spin update nixpkgs
spin updateall
```

Tarball URLs from `channels.nixos.org`, `releases.nixos.org`, `github.com`,
`gitlab.com`, `bitbucket.org`, `codeberg.org`, and `sr.ht` are supported.

In your Nix code:

```nix
let
  pins = import ./Pins.nix;
  pkgs = import pins.nixpkgs { };
in
do-something-with pkgs
```

To pin nixpkgs for my NixOS configs, I do something like:

```nix
let
  pins = import ./Pins.nix;
in
import (pins.nixpkgs + "/nixos") {
  configuration = ./my-system.nix;
  specialArgs = { inherit pins; };
}
```

then I can refer to `pins...` from all of my modules.

## NixOS integration

I use a module like this to set up `NIX_PATH` so that it refers to the pins of the current
system:

```nix
{ pins, ... }:
{
  # Indirect through /run/current-system so that when we switch systems,
  # existing processes with NIX_PATH in their environment see the new version.
  nix.nixPath = builtins.map (n: "${n}=/run/current-system/pins/${n}") (builtins.attrNames pins);
  nix.channel.enable = false;
  system.systemBuilderCommands = builtins.concatStringsSep "\n" (
    builtins.attrValues (builtins.mapAttrs (n: v: "mkdir $out/pins && ln -s ${v} $out/pins/${n}") pins)
  );
}
```

## Help, it broke

`export SPIN_FALLBACK=1` and try whatever you tried again.

That makes it use `builtins.fetchTarball` so it should properly fall back to
direct downloads. It doesn't do this all the time because
`builtins.fetchTarball` on older Nix versions doesn't attempt substitution first
(fixed in 2.32), and Styx only kicks in at substitution time. (This could be
fixed with a lot more effort on the Nix integration side.)

