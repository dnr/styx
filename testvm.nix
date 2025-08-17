{ hostPkgs ? import <nixpkgs> { config = {}; overlays = []; }, testflags ? "", fstype ? "ext4" }:
hostPkgs.testers.runNixOSTest ({config, ...}: {
  name = "styxvmtest";
  defaults._module.args = { inherit fstype; };
  nodes.machine = ./vm-testsuite.nix;
  extraDriverArgs = let
    m = config.nodes.machine;
    origScript = "${m.system.build.vm}/bin/run-${m.networking.hostName}-vm";
    mkfs = if fstype == "ext4" then
             "${hostPkgs.e2fsprogs}/bin/mkfs.ext4"
           else if fstype == "btrfs" then
             "${hostPkgs.btrfs-progs}/bin/mkfs.btrfs"
           else throw "unknown fs type";
    newScript = hostPkgs.runCommand "testvm-start-script" {} ''
      sed -e '
        s|/nix/store/[^ /]*/bin/mkfs[.]ext4|${mkfs}|
        s|,mount_tag=nix-store|&,multidevs=remap|
      ' < ${origScript} > $out
      chmod a+x $out
    '';
  in ["--start-scripts ${newScript}"];
  testScript = ''
    machine.wait_for_unit("default.target")
    machine.succeed("runstyxtest ${testflags}")
  '';
})
