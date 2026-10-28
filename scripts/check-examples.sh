#!/bin/sh
# Validates every examples/ directory that holds .tf files against a locally
# built provider wired in through dev_overrides.
set -eu

TOFU="${TOFU:-tofu}"
source_address="tambo-ai/railway-iac-sdk"
provider_binary="terraform-provider-railway-iac-sdk"

cd "$(dirname "$0")/.."

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT HUP INT TERM

if [ ! -d examples ]; then
	echo "no examples/ directory; nothing to validate"
	exit 0
fi

mkdir -p "$tmp/bin"
go build -o "$tmp/bin/$provider_binary" .

cli_config="$tmp/tofurc"
cat >"$cli_config" <<CONFIG
provider_installation {
  dev_overrides {
    "$source_address" = "$tmp/bin"
  }
  direct {}
}
CONFIG

dirs="$(find examples -type f -name '*.tf' -exec dirname {} \; | sort -u)"
if [ -z "$dirs" ]; then
	echo "no .tf files under examples/; nothing to validate"
	exit 0
fi

count=0
failed=0
for dir in $dirs; do
	work="$tmp/work/$(printf '%s' "$dir" | tr '/' '_')"
	mkdir -p "$work"
	cp "$dir"/*.tf "$work"/
	if ! grep -q 'required_providers' "$work"/*.tf; then
		cat >"$work/zz_required_providers.tf" <<HCL
terraform {
  required_providers {
    railway = {
      source = "$source_address"
    }
  }
}
HCL
	fi
	count=$((count + 1))
	# dev_overrides providers need no init; init would look the provider up in
	# the registry, which fails before the first release.
	if TF_CLI_CONFIG_FILE="$cli_config" "$TOFU" -chdir="$work" validate -no-color >"$work/validate.log" 2>&1; then
		echo "ok   $dir"
	else
		failed=$((failed + 1))
		echo "FAIL $dir" >&2
		cat "$work/validate.log" >&2
	fi
done

echo "validated $count example directories, $failed failed"
[ "$failed" -eq 0 ]
