{ pkgs ? import <nixpkgs> { } }:
{
  charon = pkgs.buildGoModule {
    pname = "charon";
    version = "0.0.1";
    vendorHash = "sha256-TB0zFm95H8R/tHfnjYK90TjpX5tqKnLohDBS9qXGzds=";
    src = pkgs.lib.sourceByRegex ./.. [
      "^go\.(mod|sum)$"
      "^(cmd|cmd/charon|cmd/charon/.*)$"
      "^(ci)($|/.*)"
    ];
    subPackages = [ "cmd/charon" ];
    doCheck = false;
  };
}
