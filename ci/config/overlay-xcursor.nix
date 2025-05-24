final: prev: {
  # Allow xterm to load Xcursor lib to load cursors.
  #
  # The problem is that libX11 does a dlopen on "libXcursor.so.1" to load the Xcursor library,
  # which uses the r[un]path of libX11 itself, so messing with xterm's r[un]path can't fix it.
  # Messing with libX11 will require rebuilding way more than we want. LD_PRELOAD works but
  # that's horrible and inherited by children of xterm. But: if we dlopen by absolute path first,
  # then X11's dlopen by soname will find it.
  #
  # Note: for this to work with custom cursors, you also need to set XCURSOR_PATH in your
  # environment, e.g.:
  #   export XCURSOR_PATH=$(echo $XDG_DATA_DIRS | sed -E 's,(:|$),/icons&,g')
  #
  # references:
  # https://github.com/NixOS/nixpkgs/issues/24137
  # https://github.com/NixOS/nixpkgs/pull/101064
  # https://cgit.freedesktop.org/xorg/lib/libX11/tree/src/CrGlCur.c#n74
  xterm = prev.xterm.overrideAttrs (old: {
    patches = old.patches ++ [
      (final.writeText "xterm-load-xcursor.patch" ''
        --- a/main.c
        +++ b/main.c
        @@ -94,4 +94,5 @@
         #include <graphics.h>

        +#include <dlfcn.h>
         #if OPT_TOOLBAR

        @@ -2510,4 +2512,6 @@ main(int argc, char *argv[]ENVP_ARG)
         #endif /* } TERMIO_STRUCT */

        +    // dlopen once here so it's cached when libX11 tries to dlopen it
        +    dlopen("${final.xorg.libXcursor}/lib/libXcursor.so.1", RTLD_LAZY);
             /* Init the Toolkit. */
             {
        '')
    ];
  });

  # xvfb-run (depended on by libadwaita, etc.) has a build-time dep on xterm, only used in an
  # installCheckPhase. So overriding xterm everywhere would cause a lot of rebuilds. This is an
  # easy hack to prevent that.
  xvfb-run = prev.xvfb-run.override { xterm = prev.xterm; };

  # notion depends on xterm directly and requires a rebuild anyway, so might as
  # well fix it too.
  notion = prev.notion.overrideAttrs (old: {
    patches = [
      (final.writeText "notion-load-xcursor.patch" ''
        --- a/notion/notion.c
        +++ b/notion/notion.c
        @@ -17,4 +17,5 @@
         #include <sys/stat.h>
         #include <fcntl.h>
        +#include <dlfcn.h>

         #include <libtu/util.h>
        @@ -155,4 +156,7 @@ int main(int argc, char*argv[])
             bool nodefaultconfig=FALSE;

        +    // dlopen once here so it's cached when libX11 tries to dlopen it
        +    dlopen("${final.xorg.libXcursor}/lib/libXcursor.so.1", RTLD_LAZY);
        +
             libtu_init(argv[0]);

        '')
    ];
  });
}
