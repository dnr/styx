{ pkgs ? import <nixpkgs> {} }:
pkgs.mkShell {
  buildInputs = with pkgs; [
    #awscli2
    jq
    go
    nix
    #skopeo
    #terraform
    #xdelta
    #xz
    #gzip
    # for cbrotli:
    #brotli.dev
    #gcc
  ];
}
