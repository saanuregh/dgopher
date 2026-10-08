{
  description = "DGopher development shell";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  outputs =
    { nixpkgs, ... }:
    let
      systems = [
        "x86_64-linux"
        "aarch64-linux"
      ];
      forEach = f: nixpkgs.lib.genAttrs systems (system: f nixpkgs.legacyPackages.${system});
    in
    {
      devShells = forEach (
        pkgs:
        let
          # MyGo loads these by name when the window opens; NixOS keeps
          # them out of the default library path.
          guiLibraries = with pkgs; [
            gtk3
            cairo
            libepoxy
            fontconfig
            harfbuzz
            libglvnd
            libx11
            libxtst
            glib
          ];
        in
        {
          default = pkgs.mkShell {
            # go.mod asks for a newer Go; the go command fetches it.
            packages = [ pkgs.go ];
            LD_LIBRARY_PATH = pkgs.lib.makeLibraryPath guiLibraries;
            # DuckDB's driver is a cgo package; mygo keeps CGO_ENABLED when set.
            CGO_ENABLED = "1";
            # GTK's file chooser aborts the app without its settings schemas.
            shellHook = ''
              export XDG_DATA_DIRS=${pkgs.gtk3}/share/gsettings-schemas/${pkgs.gtk3.name}:${pkgs.gsettings-desktop-schemas}/share/gsettings-schemas/${pkgs.gsettings-desktop-schemas.name}''${XDG_DATA_DIRS:+:$XDG_DATA_DIRS}
            '';
          };
        }
      );
    };
}
