{ pkgs ? import <nixpkgs> { config = {}; overlays = []; }, testflags ? "" }:
pkgs.testers.runNixOSTest {
  name = "styxvmtest";
  nodes.machine = ./vm-testsuite.nix;
  testScript = ''
    machine.wait_for_unit("default.target")
    machine.succeed("runstyxtest ${testflags}")
  '';
}
