# This is part of 'spin'. Don't edit here.
# See https://github.com/dnr/styx/tree/main/cmd/spin
let
  source' = builtins.fromJSON (builtins.readFile ./Pins.json);
  source =
    assert source'."$storeDir" == builtins.storeDir;
    assert source'."$version" == "spin-1";
    source';
  asRawDerivation =
    p:
    derivation {
      name = p.storePathName;
      system = builtins.currentSystem;
      builder = "not buildable: run `spin refresh --all` or set `SPIN_FALLBACK=1` and try again";
      outputHash = p.outputHash;
      outputHashMode = "recursive";
    };
  asFetchTarball =
    p:
    builtins.fetchTarball {
      name = p.storePathName;
      url = p.resolvedUrl;
      sha256 =
        assert builtins.substring 0 6 p.outputHash == "sha256";
        builtins.substring 7 64 p.outputHash;
    };
  useFallback = builtins.getEnv "SPIN_FALLBACK" != "";
  toDrv = if useFallback then asFetchTarball else asRawDerivation;
  pins = builtins.listToAttrs (
    builtins.map (p: {
      name = p.name;
      value = toDrv p;
    }) source.pins
  );
in
pins
