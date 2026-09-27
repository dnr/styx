{
  description = "Styx: on-demand and differential fetching for the Nix store";

  inputs.nixpkgs.url = "https://channels.nixos.org/nixos-26.05/nixexprs.tar.xz";

  outputs =
    { self, nixpkgs }:
    let
      forAllSystems = nixpkgs.lib.genAttrs [
        "x86_64-linux"
        "aarch64-linux"
      ];
      perSystem = forAllSystems (
        system:
        let
          pkgs = import nixpkgs { inherit system; };
          styx = import ./. { inherit pkgs; };
          mkVM =
            fstype:
            import ./runvm.nix {
              hostPkgs = pkgs;
              inherit fstype;
            };
        in
        {
          packages = styx.exportedPackages // {
            default = styx.styx-local;
            styx = styx.styx-local;
            vm = mkVM "ext4";
            vm-btrfs = mkVM "btrfs";
            vm-xfs = mkVM "xfs";
          };
          checks = nixpkgs.lib.genAttrs [ "ext4" "btrfs" "xfs" ] (
            fstype:
            import ./testvm.nix {
              hostPkgs = pkgs;
              inherit fstype;
            }
          );
        }
      );
    in
    {
      nixosModules.default = self.nixosModules.styx;
      nixosModules.styx = ./module;
      packages = forAllSystems (system: perSystem.${system}.packages);
      checks = forAllSystems (system: perSystem.${system}.checks);
    };
}
