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
      builder = "/bin/false";
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
  disableFallback = builtins.getEnv "SPIN_DISABLE_FALLBACK" != "";
  toDrv = if disableFallback then asRawDerivation else asFetchTarball;
  pins = builtins.listToAttrs (
    builtins.map (p: {
      name = p.name;
      value = toDrv p;
    }) source.pins
  );
in
pins
