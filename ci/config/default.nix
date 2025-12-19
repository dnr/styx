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
  boot.kernelPackages = pkgs.linuxPackages_latest;

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

  environment.systemPackages = with pkgs; [

    ascii
    awscli2
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
    dragon-drop
    dunst
    easyeffects
    evince
    ffmpeg
    file
    gcc
    gdb
    gh
    gimp
    git
    git-absorb
    gnome-icon-theme
    gnupg
    go
    gocryptfs
    (google-chrome.override { speechd-minimal = snappy; }) # hack to avoid bringing in speech deps. non-redistributable?
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
    protobuf
    protoc-gen-go
    psmisc
    pulseaudio
    pv
    python3
    redshift
    ripgrep
    rsync
    screen
    scrot
    signal-desktop
    skopeo
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
    pkgs.noto-fonts-color-emoji
    pkgs.ubuntu-classic
    pkgs.nerd-fonts.ubuntu-mono
  ];

  documentation.nixos.enable = false;

  system.stateVersion = "25.11";
}
