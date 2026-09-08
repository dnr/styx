let
  pins = import ./Pins.nix;
in
{
  hostPkgs ? import pins.nixpkgs {
    config = { };
    overlays = [ ];
  },
  testflags ? "",
  fstype ? "ext4",
}:
hostPkgs.testers.runNixOSTest (
  { config, lib, ... }:
  {
    name = "styxvmtest";
    defaults._module.args = { inherit fstype; };
    nodes.machine = ./vm-testsuite.nix;
    driverConfiguration.vms.machine.start_script =
      let
        m = config.nodes.machine;
        origScript = "${m.system.build.vm}/bin/run-${m.networking.hostName}-vm";
        mkfs =
          if fstype == "ext4" then
            "${hostPkgs.e2fsprogs}/bin/mkfs.ext4"
          else if fstype == "btrfs" then
            "${hostPkgs.btrfs-progs}/bin/mkfs.btrfs"
          else
            throw "unknown fs type";
        newScript = hostPkgs.runCommand "testvm-start-script" { } ''
          sed -e '
            s|/nix/store/[^ /]*/bin/mkfs[.]ext4|${mkfs}|
            s|,mount_tag=nix-store|&,multidevs=remap|
            s|-m 1024|-m 4096|
            /memory-backend/ s|1024M|4096M|
          ' < ${origScript} > $out
          chmod a+x $out
        '';
      in
      lib.mkForce newScript;
    testScript = ''
      machine.wait_for_unit("default.target")
      machine.succeed("runstyxtest ${testflags}")
    '';
  }
)
