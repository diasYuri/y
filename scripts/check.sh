#!/usr/bin/env bash
#
# Run the formatting, vet, and test checks expected by CI.
#
# Usage:
#   scripts/check.sh           # full suite (gofmt + go vet + go test + tagged tests)
#   scripts/check.sh fmt       # gofmt only (fails if any file has diffs)
#   scripts/check.sh vet       # go vet ./...
#   scripts/check.sh test      # go test ./... (default tags)
#   scripts/check.sh test-all  # go test ./... with every feature_* tag enabled
#   scripts/check.sh build     # build verification across reference profiles
#
# All checks run from the repository root. The fmt step fails non-zero if any
# Go file is not gofmt-clean and prints the offending paths to stderr.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
GO="${GO:-go}"

step="${1:-all}"

run_fmt() {
	echo "check.sh: gofmt -l (verifying formatting)"
	# gofmt -l returns 0 even when there are diffs; we treat any output as failure.
	local diff
	diff="$(cd "${REPO_ROOT}" && gofmt -l . | grep -v '^\.gomodcache/' | grep -v '^\.extract/' || true)"
	if [ -n "${diff}" ]; then
		{
			echo "check.sh: gofmt found unformatted files:"
			while IFS= read -r path; do
				echo "  ${path}"
			done <<< "${diff}"
			echo "check.sh: run 'gofmt -w <path>' or 'gofmt -w .' to fix."
		} >&2
		return 1
	fi
	echo "check.sh: gofmt clean"
}

run_vet() {
	echo "check.sh: go vet ./..."
	(cd "${REPO_ROOT}" && "${GO}" vet ./...)
}

run_test() {
	echo "check.sh: go test ./..."
	(cd "${REPO_ROOT}" && "${GO}" test ./...)
}

# Tag set covering every feature_* in internal/feature/catalog.go. Running tests
# under this set ensures tag-gated code (WASM host, mom/pods bundles)
# stays compilable.
all_feature_tags="feature_fs feature_git feature_shell feature_lsp feature_rpc feature_telemetry feature_mom feature_pods feature_wasm_ext feature_openai feature_anthropic feature_google feature_local feature_storage_sqlite"

run_test_all() {
	echo "check.sh: go test ./... -tags=\"${all_feature_tags}\""
	(cd "${REPO_ROOT}" && "${GO}" test -tags "${all_feature_tags}" ./...)
}

# Build verification touches each reference flavor for the host platform so a
# missing tag combination (e.g. a stub disabled file deleted by mistake) is
# caught before release.
run_build() {
	echo "check.sh: build verification (host platform, all flavors)"
	local script="${SCRIPT_DIR}/build.sh"
	if [ ! -x "${script}" ]; then
		chmod +x "${script}" || true
	fi
	"${script}" --binary all --flavor minimal
	"${script}" --binary all --flavor standard
	"${script}" --binary all --flavor full
}

case "${step}" in
	fmt)
		run_fmt
		;;
	vet)
		run_vet
		;;
	test)
		run_test
		;;
	test-all)
		run_test_all
		;;
	build)
		run_build
		;;
	all)
		run_fmt
		run_vet
		run_test
		run_test_all
		;;
	*)
		echo "check.sh: unknown step: ${step}" >&2
		echo "Run scripts/check.sh fmt|vet|test|test-all|build|all" >&2
		exit 2
		;;
esac
