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
    driverConfiguration.vms.machine.start_script = lib.mkForce (
      lib.getExe config.nodes.machine.system.build.styxVm
    );
    testScript = ''
      machine.wait_for_unit("default.target")
      machine.succeed("runstyxtest ${testflags}")
    '';
  }
)
