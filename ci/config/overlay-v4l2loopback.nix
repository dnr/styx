final: prev: {
  linuxPackages_latest = prev.linuxPackages_latest.extend (
    lpFinal: lpPrev: {
      v4l2loopback =
        let
          version = "0.15.3";
        in
        lpPrev.v4l2loopback.overrideAttrs (old: {
          version = "${version}-${lpPrev.kernel.version}";
          src = final.fetchFromGitHub {
            owner = "umlaeute";
            repo = "v4l2loopback";
            tag = "v${version}";
            hash = "sha256-KXJgsEJJTr4TG4Ww5HlF42v2F1J+AsHwrllUP1n/7g8=";
          };
        });
    }
  );
}
