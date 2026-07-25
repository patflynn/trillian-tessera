{
  description = "Dev tooling for Tessera (Go toolchain + cloud SDKs, plus tofu/terragrunt for the deployment/ IaC stacks)";

  # Reuse the host's pinned nixpkgs so `nix develop` is instant. Override with
  # `--override-input nixpkgs github:NixOS/nixpkgs/nixpkgs-unstable` for portability.
  inputs.nixpkgs.url = "flake:nixpkgs";

  outputs = { self, nixpkgs }:
    let
      systems = [ "x86_64-linux" "aarch64-linux" "x86_64-darwin" "aarch64-darwin" ];
      forAllSystems = f: nixpkgs.lib.genAttrs systems (system: f nixpkgs.legacyPackages.${system});
    in
    {
      devShells = forAllSystems (pkgs: {
        # `nix develop` -> tooling for the cloud storage backends and conformance
        # lanes, plus tofu/terragrunt for the deployment/ IaC stacks.
        default = pkgs.mkShell {
          packages = [
            pkgs.google-cloud-sdk # gcloud, gsutil
            pkgs.gh               # GitHub CLI (repo vars, workflows)
            pkgs.golangci-lint    # Go linter — matches the `lint` CI job
            pkgs.opentofu         # `tofu` — open-source Terraform drop-in (terraform is unfree in nixpkgs)
            pkgs.terragrunt       # wrapper over tofu for the deployment/ IaC stacks
          ];

          # terragrunt otherwise looks for a `terraform` binary, which is absent/unfree; point it at tofu.
          TERRAGRUNT_TFPATH = "${pkgs.opentofu}/bin/tofu";
        };
      });
    };
}
