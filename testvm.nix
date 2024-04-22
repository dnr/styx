{ pkgs ? import <nixpkgs> { config = {}; overlays = []; } }:
pkgs.testers.runNixOSTest {
  name = "styxvmtest";
  nodes.machine = import ./vm.nix;
  testScript = ''
    machine.wait_for_unit("default.target")
    machine.succeed("runstyxtest")
  '';
}
