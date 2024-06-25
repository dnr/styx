# Stripped-down version of my (dnr)'s basic nix config:

{ pkgs, ... }:
{
  imports = [
    <styx/module>
  ];

  # builds custom kernel, patched nix, styx binary
  services.styx.enable = true;

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
    gocryptfs
    google-chrome
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
    luminanceHDR
    lzma
    magic-wormhole
    mercurial
    moreutils
    mplayer
    nix-direnv
    nixos-option
    nixpkgs-fmt
    nodejs
    notion
    nvme-cli
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
    smem
    socat
    spotify
    sqlite
    starship
    strace
    sxiv
    sysstat
    tcpdump
    tig
    tree
    unzip
    usbutils
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
    zip
    zoom-us
    zoxide
    zstd

  ];

  services.fprintd.enable = true;
  services.fwupd.enable = true;
  services.tlp.enable = true;
  services.xserver.enable = true;
  services.zerotierone.enable = true;

  fonts.packages = [
    pkgs.noto-fonts
    pkgs.noto-fonts-cjk
    pkgs.noto-fonts-emoji
    pkgs.ubuntu_font_family
    (pkgs.nerdfonts.override { fonts = [ "UbuntuMono" ]; })
  ];

  documentation.nixos.enable = false;

  system.stateVersion = "20.09";
}
