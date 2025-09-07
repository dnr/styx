let
  overlays =
    if builtins.getEnv "USE_NIX_GOCACHEPROG" == "" then
      [ ]
    else
      [
        (import "${
          fetchTarball {
            url = "https://github.com/dnr/nix-gocacheprog/archive/349d679ae547.tar.gz";
            sha256 = "1c2dkrlc2qym8y6ls40ksxl3x35xdml0yd1m6y4lj91dxa15c1af";
          }
        }/overlay.nix")
      ];
in
{
  pkgs ? import <nixpkgs> {
    config = { };
    overlays = overlays;
  },
}:
rec {
  base = {
    pname = "styx";
    version = "0.0.11";
    vendorHash = "sha256-WJtuxe8A6VCxUVgvEoLp1XQhpSOk1W99/w56mtodLRA=";
    src = pkgs.lib.sourceByRegex ./. [
      "^go\\.(mod|sum)$"
      "^(ci|cmd|common|daemon|erofs|manifester|pb|keys|tests)($|/.*)"
    ];
    subPackages = [ "cmd/styx" ];
    doCheck = false;
    ldflags = baseLdFlags;
  };

  baseLdFlags = [
    "-X github.com/dnr/styx/common.NixBin=${pkgs.nix}/bin/nix"
    "-X github.com/dnr/styx/common.XzBin=${pkgs.xz}/bin/xz"
    "-X github.com/dnr/styx/common.Version=${base.version}"
  ];
  daemonLdFlags = baseLdFlags ++ [
    "-X github.com/dnr/styx/common.GzipBin=${pkgs.gzip}/bin/gzip"
    "-X github.com/dnr/styx/common.FilefragBin=${pkgs.e2fsprogs}/bin/filefrag"
  ];
  staticLdFlags = [
    # "-s" "-w"  # only saves 3.6% of image size
    "-X github.com/dnr/styx/common.XzBin=${xzStaticBin}/bin/xz"
    # GzipBin is not used by manifester or differ, only local
    "-X github.com/dnr/styx/common.Version=${base.version}"
  ];

  styx-local = pkgs.buildGoModule (
    base
    // {
      ldflags = daemonLdFlags;
    }
  );

  styx-lambda = pkgs.buildGoModule (
    base
    // {
      tags = [ "lambda.norpc" ];
      ldflags = staticLdFlags;
    }
  );

  styx-test = pkgs.buildGoModule (
    base
    // {
      pname = "styxtest";
      buildPhase = ''
        go test -ldflags="$ldflags" -c -o styxtest ./tests
      '';
      installPhase = ''
        mkdir -p $out/bin $out/keys
        install styxtest $out/bin/
        cp keys/testsuite* $out/keys/
      '';
      ldflags = daemonLdFlags ++ [
        "-X github.com/dnr/styx/tests.TestdataDir=${testdata}"
      ];
    }
  );

  # TODO: switch to nixVersions.stable
  patchedNix = pkgs.nixVersions.nix_2_24.overrideAttrs (prev: {
    patches = prev.patches ++ [ ./patches/nix_2_24.patch ];
    doInstallCheck = false; # broke tests/ca, ignore for now
  });

  testdata =
    let
      paths = [
        "cd1nbildgzzfryjg82njnn36i4ynyf8h-bash-interactive-5.1-p16-man"
        "8vyj9c6g424mz0v3kvzkskhvzhwj6288-bash-interactive-5.2-p15-man"
        "3a7xq2qhxw2r7naqmc53akmx7yvz0mkf-less-is-more.patch"
        "v35ysx9k1ln4c6r7lj74204ss4bw7l5l-openssl-3.0.12-man"
        "xd96wmj058ky40aywv72z63vdw9yzzzb-openssl-3.0.12-man"
        "1fka6ngkrlmqkhix0gnnb19z58sr0yma-openssl-3.0.13-man"
        "z2waz77lsh4pxs0jxgmpf16s7a3g7b7v-openssl-3.0.13-man"
        "53qwclnym7a6vzs937jjmsfqxlxlsf2y-opusfile-0.12"
        "kcyrz2y8si9ry5p8qkmj0gp41n01sa1y-opusfile-0.12"
        "qa22bifihaxyvn6q2a6w9m0nklqrk9wh-opusfile-0.12"
        "xpq4yhadyhazkcsggmqd7rsgvxb3kjy4-gnugrep-3.11"
        "kbi7qf642gsxiv51yqank8bnx39w3crd-calf-0.90.3" # 18 MB
        "d30xd6x3669hg2a6xwjb1r3nb9a99sw2-openblas-0.3.27" # 27 MB
      ];
      hash = "sha256-eSl0bAOueksPX4szbeoqLa3OfUbNtxxOGvfHeoymhmY=";
    in
    pkgs.stdenv.mkDerivation {
      name = "styx-test-data";
      builder = pkgs.writeShellScript "build-testdata" ''
        set -e; . .attrs.sh
        PATH=${pkgs.coreutils}/bin:${pkgs.gnused}/bin:${pkgs.wget}/bin:$PATH
        export NIX_SSL_CERT_FILE=${pkgs.cacert}/etc/ssl/certs/ca-bundle.crt
        out=''${outputs[out]}
        mkdir $out && cd $out
        ${pkgs.lib.concatMapStringsSep "\n" (p: ''
          p=${p}; ni=''${p%%-*}.narinfo
          wget -nv -x -nH https://cache.nixos.org/$ni
          wget -nv -x -nH https://cache.nixos.org/$(sed -ne '/URL/s/.* //p' $ni)
        '') paths}
      '';
      __structuredAttrs = true; # needed for unsafeDiscardReferences
      # this is pure data just for tests, even if references happen to match
      unsafeDiscardReferences.out = true;
      outputHashMode = "recursive";
      outputHash = hash;
    };

  # built by deploy-ci, for heavy CI worker on EC2
  charon = pkgs.buildGoModule (
    base
    // {
      pname = "charon";
      subPackages = [ "cmd/charon" ];
    }
  );
  # for light CI worker on non-AWS server (dd5):
  charon-light = pkgs.buildGoModule (
    base
    // {
      pname = "charon";
      subPackages = [ "cmd/charon" ];
      ldflags = [ ];
    }
  );

  # Use static binaries and take only the main binaries to make the image as
  # small as possible:
  xzStaticBin = pkgs.stdenv.mkDerivation {
    name = "xz-binonly";
    src = pkgs.pkgsStatic.xz;
    installPhase = "mkdir -p $out/bin && cp $src/bin/xz $out/bin/";
  };

  # for styx lambda manifester and chunk differ:
  styx-lambda-image = pkgs.dockerTools.streamLayeredImage {
    name = "lambda";
    # TODO: can we make it run on arm?
    # architecture = "arm64";
    contents = [
      pkgs.cacert
      axiom-lambda-extension
    ];
    config = {
      User = "1000:1000";
      Entrypoint = [ "${styx-lambda}/bin/styx" ];
    };
  };

  # for light CI worker on non-AWS server (dd5):
  charon-image = pkgs.dockerTools.streamLayeredImage {
    name = "charon-worker-light";
    contents = [
      pkgs.cacert
    ];
    config = {
      User = "1000:1000";
      Entrypoint = [ "${charon-light}/bin/charon" ];
    };
  };

  # axiom lambda extension
  # TODO: maybe we can build this into our binary to reduce the total size?
  axiom-lambda-extension =
    let
      version = "v11";
    in
    pkgs.buildGoModule {
      pname = "axiom-lambda-extension";
      inherit version;
      vendorHash = "sha256-f+Z5ETHFNuM+QimZFySmFtSVG0Qaw5HAI9032rNyXqc=";
      src = pkgs.fetchFromGitHub {
        owner = "axiomhq";
        repo = "axiom-lambda-extension";
        rev = version;
        hash = "sha256-lax5MvyF0u6susJNjddIFQuciYEhQxuYmdYelcdupb0=";
      };
      postInstall = ''
        cd $out
        mkdir opt
        mv bin opt/extensions
      '';
    };

  # helper to initialize styx with "test-1" params
  StyxInitTest1 = pkgs.writeShellScriptBin "StyxInitTest1" ''
    styx init --params=https://styx-1.s3.amazonaws.com/params/test-1 --styx_pubkey=styx-test-1:bmMrKgN5yF3dGgOI67TZSfLts5IQHwdrOCZ7XHcaN+w=
  '';
}
