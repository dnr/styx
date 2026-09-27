{
  config,
  lib,
  pkgs,
  ...
}:
{
  assertions = [
    {
      assertion = config.virtualisation.diskImage != null;
      message = "must use disk image";
    }
  ];
  # supported filesystems must be available
  system.requiredKernelConfig = with config.lib.kernelConfig; [
    (isEnabled "BTRFS_FS")
    (isEnabled "XFS_FS")
  ];

  # vm launch script hacks:
  # - add multidevs=remap to make qemu able to share directories with nested
  #   mounts, to support a styx-enabled nix store on the host
  # - replace the mkfs command to use other fs types
  system.build.styxVm =
    let
      hostPkgs = config.virtualisation.host.pkgs;
      vm = config.system.build.vm;
      program = vm.meta.mainProgram;
      fstype = config.virtualisation.fileSystems."/".fsType;
      mkfs =
        {
          ext4 = "${hostPkgs.e2fsprogs}/bin/mkfs.ext4";
          btrfs = "${hostPkgs.btrfs-progs}/bin/mkfs.btrfs";
          xfs = "${hostPkgs.xfsprogs.bin}/bin/mkfs.xfs";
        }
        .${fstype} or (throw "unsupported VM root filesystem: ${fstype}");
    in
    hostPkgs.runCommand "styx-vm-${fstype}" { meta.mainProgram = program; } ''
      mkdir -p $out/bin
      ln -s ${vm}/system $out/system
      sed \
        -e 's|,mount_tag=nix-store|&,multidevs=remap|' \
        -e 's|/nix/store/[^ /]*/bin/mkfs[.]ext4|${mkfs}|' \
        ${vm}/bin/${program} > $out/bin/${program}
      chmod +x $out/bin/${program}
    '';

  boot.loader.systemd-boot.enable = true;
  boot.loader.efi.canTouchEfiVariables = true;
  boot.kernelPackages = pkgs.linuxPackages_latest;

  networking.hostName = "testvm";
  networking.networkmanager.enable = true;

  users.users.test = {
    isNormalUser = true;
    initialPassword = "test";
    extraGroups = [ "wheel" ];
  };

  security.sudo.wheelNeedsPassword = false;

  documentation.doc.enable = false;
  documentation.info.enable = false;
  documentation.man.enable = false;
  documentation.nixos.enable = false;

  system.stateVersion = "23.11";
}
