{
  description = "fanin: the daemon that fans coding-agent work out to worker hosts";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  outputs = { self, nixpkgs }:
    let
      systems = [ "x86_64-linux" "aarch64-linux" "aarch64-darwin" "x86_64-darwin" ];
      forAll = f: nixpkgs.lib.genAttrs systems (system: f nixpkgs.legacyPackages.${system});
    in
    {
      devShells = forAll (pkgs: {
        default = pkgs.mkShell {
          packages = [
            pkgs.go_1_26
            pkgs.golangci-lint
            pkgs.govulncheck
            pkgs.shellcheck
            pkgs.gnumake
            pkgs.git
          ];
          shellHook = "unset GOROOT";
        };
      });

      packages = forAll (pkgs: {
        default = pkgs.buildGoModule {
          pname = "fanin";
          version = "0.0.0";
          src = self;
          go = pkgs.go_1_26;
          vendorHash = null;
          subPackages = [ "cmd/fanind" "cmd/fanin" ];
        };
      });
    };
}
