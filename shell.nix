{ pkgs ? import <nixpkgs> {} }:
pkgs.mkShell {
  buildInputs = with pkgs; [
    #awscli2
    erofs-utils
    jq
    go
    nix
    protobuf
    protoc-gen-go
    skopeo
    terraform
    #xdelta
    #xz
    #gzip
    # for cbrotli:
    #brotli.dev
    #gcc
  ];
}
