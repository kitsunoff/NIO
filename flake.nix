{
  description = "NIO — Nix-native Kubernetes operator: pinned developer shell";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  outputs = { self, nixpkgs }:
    let
      systems = [ "x86_64-linux" "aarch64-linux" "x86_64-darwin" "aarch64-darwin" ];
      forAllSystems = f: nixpkgs.lib.genAttrs systems (system: f nixpkgs.legacyPackages.${system});
    in
    {
      devShells = forAllSystems (pkgs: {
        default = pkgs.mkShell {
          name = "nio-dev";
          # Tooling the Makefile expects on PATH. controller-gen, setup-envtest,
          # and kustomize are still fetched into ./bin by the Makefile.
          packages = with pkgs; [
            go
            gopls
            golangci-lint
            kubectl
            kubernetes-helm
            kind
            git
            gnumake
          ];
        };
      });
    };
}
