# Stripped-down version of my (dnr)'s basic nix config.
# If you're actually using this and want more stuff in here, let me know.

{ config, pkgs, ... }: {
  #boot.kernelPackages = pkgs.linuxPackages_latest;

  # just enough to make nix-build not complain:
  fileSystems."/".device = "/dev/dummy";
  boot.loader.grub.device = "nodev";

  hardware.enableRedistributableFirmware = true;
  networking.networkmanager.enable = true;
  #nixpkgs.config.allowUnfree = true;

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
    firefox
    gdb
    gh
    gimp
    git
    git-absorb
    gnome-icon-theme
    gnupg
    go
    gocryptfs
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
    zoxide
    zstd

  ];

  services.fwupd.enable = true;
  #services.tlp.enable = true;
  services.xserver.enable = true;

  fonts.packages = [
    pkgs.noto-fonts
    pkgs.noto-fonts-cjk-sans
    pkgs.noto-fonts-emoji
    pkgs.ubuntu_font_family
    #pkgs.nerd-fonts.ubuntu-mono
  ];

  documentation.nixos.enable = false;

  system.stateVersion = "25.05";
}
