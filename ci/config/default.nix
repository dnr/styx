# Stripped-down version of my (dnr)'s basic nix config.
# If you're actually using this and want more stuff in here, let me know.

{ config, pkgs, ... }:
{
  imports = [
    <styx/module>
  ];

  # builds custom kernel, patched nix, styx binary
  services.styx.enable = true;

  # build with latest kernel to get >= 6.8
  #boot.kernelPackages = pkgs.linuxPackages_latest;

  # TODO: temp downgrade to 6.14.6 for amdgpu bug
  boot.kernelPackages = let
    version = "6.14.6";
    src = pkgs.fetchurl {
      url = "mirror://kernel/linux/kernel/v6.x/linux-${version}.tar.xz";
      hash = "sha256:1acpjxscw5nvgp19jzd29zhl1c6wdzx0bxp4yy0hy8z2k0cpz091";
    };
    argsOverride = {
      inherit src version;
      modDirVersion = version;
    };
    kernel = pkgs.linux_6_14.override { inherit argsOverride; };
  in pkgs.linuxPackagesFor kernel;

  # some kernel modules that I use that depend on the custom kernel:
  boot.extraModulePackages = with config.boot.kernelPackages; [
    acpi_call
    v4l2loopback
  ];

  # just enough to make nix-build not complain:
  fileSystems."/".device = "/dev/dummy";
  boot.loader.grub.device = "nodev";

  hardware.enableRedistributableFirmware = true;
  networking.networkmanager.enable = true;
  nixpkgs.config.allowUnfree = true;
  nixpkgs.overlays = [
    (import ./overlay-xcursor.nix)
  ];

  environment.systemPackages = with pkgs;
  let
    pythonWithMyPkgs = python3.withPackages (pp: with pp; [
      dbus-python
      pyserial
      requests
      websocket_client
    ]);
  in [

    ascii
    bc
    binutils-unwrapped
    borgbackup
    brotli
    btrfs-progs
    compsize
    cryptsetup
    curl
    darktable
    ddcutil
    diffstat
    diffutils
    direnv
    dmenu
    docker
    docker-compose
    dunst
    easyeffects
    evince
    ffmpeg
    file
    gdb
    gh
    gimp
    git
    git-absorb
    gnome-icon-theme
    gnupg
    go
    gocryptfs
    (google-chrome.override { speechd-minimal = snappy; })  # hack to avoid bringing in speech deps. non-redistributable?
    guvcview
    hdparm
    hugin
    imagemagick
    jq
    libnotify
    libsecret
    lm_sensors
    lsof
    ltrace
    magic-wormhole
    moreutils
    mplayer
    mpv
    nix-direnv
    nixos-option
    nixpkgs-fmt
    nodejs
    notion
    nvme-cli
    obs-studio
    openssh
    openssl
    opusTools
    pavucontrol
    pciutils
    pipewire
    psmisc
    pulseaudio
    pv
    pythonWithMyPkgs
    redshift
    ripgrep
    rsync
    screen
    scrot
    signal-desktop
    smem
    socat
    spacer
    spotify # non-redistributable?
    sqlite
    starship
    strace
    sxiv
    sysstat
    tcpdump
    terraform # non-redistributable?
    tig
    tree
    unzip
    usbutils
    v4l-utils
    vim
    wget
    wireguard-tools
    wireplumber
    xdotool
    xdragon
    xosd
    xsel
    xsettingsd
    xxd
    xz
    yt-dlp
    zip
    zoom-us # non-redistributable?
    zoxide
    zstd

  ];

  services.fprintd.enable = true;
  services.fwupd.enable = true;
  services.tlp.enable = true;
  services.xserver.enable = true;
  services.zerotierone.enable = true; # non-redistributable?

  fonts.packages = [
    pkgs.noto-fonts
    pkgs.noto-fonts-cjk-sans
    pkgs.noto-fonts-emoji
    pkgs.ubuntu_font_family
    pkgs.nerd-fonts.ubuntu-mono
  ];

  documentation.nixos.enable = false;

  system.stateVersion = "25.05";
}
