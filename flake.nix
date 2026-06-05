{
  description = "Dev tooling for the patflynn/trillian-tessera fork (GCP CI setup)";

  # Reuse the host's registry-pinned nixpkgs so `nix develop` is instant (no extra download).
  # Override with `--override-input nixpkgs github:NixOS/nixpkgs/nixpkgs-unstable` for a portable build.
  inputs.nixpkgs.url = "flake:nixpkgs";

  outputs = { self, nixpkgs }:
    let
      systems = [ "x86_64-linux" "aarch64-linux" "x86_64-darwin" "aarch64-darwin" ];
      forAllSystems = f: nixpkgs.lib.genAttrs systems (system: f nixpkgs.legacyPackages.${system});
    in
    {
      devShells = forAllSystems (pkgs: {
        # `nix develop` -> gcloud / gsutil for the GCP CI setup runbook (docs/design/gcp-ci-setup.md).
        default = pkgs.mkShell {
          packages = [
            pkgs.google-cloud-sdk # gcloud, gsutil
            pkgs.gh               # GitHub CLI (repo vars, workflows, branch protection)
          ];
        };
      });
    };
}
