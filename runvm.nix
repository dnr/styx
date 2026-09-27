let
  pins = import ./Pins.nix;
in
{
  hostPkgs ? import pins.nixpkgs { },
  fstype ? "ext4",
}:
let
  os = hostPkgs.nixos [
    ./vm-interactive.nix
    {
      virtualisation.fileSystems."/".fsType = hostPkgs.lib.mkForce fstype;
      virtualisation.diskImage = "./styx-vm-${fstype}.qcow2";
    }
  ];
in
os.config.system.build.styxVm
