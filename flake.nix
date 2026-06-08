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
        # `nix develop` -> tooling for the GCP CI setup runbook (docs/design/gcp-ci-setup.md)
        # and the Terraform/Terragrunt stacks that provision/tear down the CI infra.
        default = pkgs.mkShell {
          packages = [
            pkgs.google-cloud-sdk # gcloud, gsutil
            pkgs.gh               # GitHub CLI (repo vars, workflows, branch protection)
            pkgs.opentofu         # `tofu` — open-source (MPL) Terraform drop-in; terraform itself is unfree (BSL) in nixpkgs
            pkgs.terragrunt       # thin wrapper over tofu for the CI infra stacks
            pkgs.golangci-lint    # Go linter — matches the `lint` CI job; run locally before pushing (uses the system Go toolchain)
          ];
          # Point Terragrunt at OpenTofu instead of the (unfree, absent) terraform binary.
          TERRAGRUNT_TFPATH = "${pkgs.opentofu}/bin/tofu";
        };
      });
    };
}
