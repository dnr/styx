{ pkgs ? import <nixpkgs> {} }:
pkgs.mkShell {
  buildInputs = with pkgs; [
    awscli2
    erofs-utils
    jq
    go
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
