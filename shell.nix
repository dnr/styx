{
  pkgs ? import <nixpkgs> { },
}:
pkgs.mkShell {
  buildInputs = with pkgs; [
    awscli2
    #brotli.dev # for cbrotli
    #erofs-utils
    #gcc
    go
    #gzip
    jq
    protobuf
    protoc-gen-go
    skopeo
    terraform
    #xdelta
    #xz
  ];
}
