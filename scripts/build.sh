#!/usr/bin/env bash
#
# Cross-platform build script for y binaries.
#
# Produces artefacts under bin/ named:
#   <binary>-<flavor>-<os>-<arch>[.exe]
#
# Usage:
#   scripts/build.sh [options]
#
# Options:
#   --binary <name>    Binary to build (y|y-mom|y-pods|all). Default: y.
#   --flavor <name>    Build flavor (minimal|standard|full|custom). Default: standard.
#   --tags "<tags>"    Override build tags (forces flavor=custom).
#   --os <list>        Comma-separated GOOS list. Default: host OS.
#   --arch <list>      Comma-separated GOARCH list. Default: host ARCH.
#   --matrix           Build all reference OS/arch targets.
#   --version <ver>    Version string injected via ldflags. Default: VERSION env or 0.0.0-dev.
#   --commit <sha>     Commit SHA injected via ldflags. Default: git or "unknown".
#   --date <iso>       Build date injected via ldflags. Default: current UTC ISO 8601.
#   --output-dir <p>   Output directory. Default: bin.
#   --dry-run          Print the commands that would run without executing them.
#   --help             Show this help.
#
# Environment overrides: VERSION, COMMIT, BUILD_DATE, GO, OUTPUT_DIR.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
GO="${GO:-go}"

print_help() {
	sed -n '2,/^$/p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
}

# Reference OS/arch matrix matches the release artefact list in docs/release.md.
matrix_targets=(
	"darwin/arm64"
	"darwin/amd64"
	"linux/amd64"
	"linux/arm64"
	"windows/amd64"
)

# Build flavors. Each flavor lists the binaries it produces and the build tags
# applied to each. A binary with no tag entry is skipped for that flavor.
flavor_tags_minimal_y="feature_fs feature_openai"

flavor_tags_standard_y="feature_fs feature_git feature_shell feature_openai feature_anthropic feature_google feature_local"
flavor_tags_standard_y_mom="feature_mom feature_anthropic feature_openai"
flavor_tags_standard_y_pods="feature_pods"

flavor_tags_full_y="feature_fs feature_git feature_shell feature_openai feature_anthropic feature_google feature_local feature_lsp feature_rpc feature_telemetry feature_wasm_ext feature_storage_sqlite"
flavor_tags_full_y_mom="feature_mom feature_anthropic feature_openai"
flavor_tags_full_y_pods="feature_pods"

binary=y
flavor=standard
custom_tags=""
os_list=""
arch_list=""
use_matrix=0
version="${VERSION:-}"
commit="${COMMIT:-}"
build_date="${BUILD_DATE:-}"
output_dir="${OUTPUT_DIR:-bin}"
dry_run=0

while [ $# -gt 0 ]; do
	case "$1" in
		--binary)
			binary="$2"
			shift 2
			;;
		--flavor)
			flavor="$2"
			shift 2
			;;
		--tags)
			custom_tags="$2"
			flavor="custom"
			shift 2
			;;
		--os)
			os_list="$2"
			shift 2
			;;
		--arch)
			arch_list="$2"
			shift 2
			;;
		--matrix)
			use_matrix=1
			shift
			;;
		--version)
			version="$2"
			shift 2
			;;
		--commit)
			commit="$2"
			shift 2
			;;
		--date)
			build_date="$2"
			shift 2
			;;
		--output-dir)
			output_dir="$2"
			shift 2
			;;
		--dry-run)
			dry_run=1
			shift
			;;
		--help|-h)
			print_help
			exit 0
			;;
		*)
			echo "build.sh: unknown option: $1" >&2
			echo "Run scripts/build.sh --help for usage." >&2
			exit 2
			;;
	esac
done

if [ -z "${version}" ]; then
	version="0.0.0-dev"
fi
if [ -z "${commit}" ]; then
	if commit_value=$(git -C "${REPO_ROOT}" rev-parse HEAD 2>/dev/null); then
		commit="${commit_value}"
	else
		commit="unknown"
	fi
fi
if [ -z "${build_date}" ]; then
	build_date="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
fi

binary_list=()
case "${binary}" in
	all)
		binary_list=(y y-mom y-pods)
		;;
	y|y-mom|y-pods)
		binary_list=("${binary}")
		;;
	*)
		echo "build.sh: unsupported --binary: ${binary} (expected y|y-mom|y-pods|all)" >&2
		exit 2
		;;
esac

if [ "${use_matrix}" -eq 1 ]; then
	if [ -n "${os_list}" ] || [ -n "${arch_list}" ]; then
		echo "build.sh: --matrix is mutually exclusive with --os/--arch" >&2
		exit 2
	fi
fi

# Resolve build tags for a binary and flavor.
tags_for() {
	local bin="$1"
	local flv="$2"
	local key
	key="$(echo "${bin}" | tr '-' '_')"
	if [ "${flv}" = "custom" ]; then
		printf '%s' "${custom_tags}"
		return 0
	fi
	local var="flavor_tags_${flv}_${key}"
	# Indirect lookup compatible with bash 3.2 (default macOS bash).
	if eval "[ -n \"\${${var}:-}\" ]"; then
		eval "printf '%s' \"\${${var}}\""
	else
		printf ''
	fi
}

# Convert an OS/ARCH pair into a binary suffix.
binary_name() {
	local bin="$1"
	local flv="$2"
	local goos="$3"
	local goarch="$4"
	local suffix=""
	if [ "${goos}" = "windows" ]; then
		suffix=".exe"
	fi
	printf '%s-%s-%s-%s%s' "${bin}" "${flv}" "${goos}" "${goarch}" "${suffix}"
}

build_one() {
	local bin="$1"
	local flv="$2"
	local goos="$3"
	local goarch="$4"
	local tags
	tags="$(tags_for "${bin}" "${flv}")"
	if [ -z "${tags}" ] && [ "${flv}" != "custom" ]; then
		echo "build.sh: skipping ${bin} for flavor ${flv} (no tag set defined)"
		return 0
	fi

	local out
	out="${REPO_ROOT}/${output_dir}/$(binary_name "${bin}" "${flv}" "${goos}" "${goarch}")"
	mkdir -p "$(dirname "${out}")"

	local ldflags
	ldflags="-s -w"
	ldflags+=" -X github.com/yuri/y/internal/buildinfo.version=${version}"
	ldflags+=" -X github.com/yuri/y/internal/buildinfo.commit=${commit}"
	ldflags+=" -X github.com/yuri/y/internal/buildinfo.date=${build_date}"
	# Tags are stored comma-separated in the binary so `y doctor` can show them.
	local tag_csv
	tag_csv="$(echo "${tags}" | tr ' ' ',')"
	ldflags+=" -X github.com/yuri/y/internal/buildinfo.tags=${tag_csv}"

	local cmd_dir="${REPO_ROOT}/cmd/${bin}"
	if [ ! -d "${cmd_dir}" ]; then
		echo "build.sh: missing command directory ${cmd_dir}" >&2
		return 1
	fi

	echo "build.sh: ${bin} flavor=${flv} GOOS=${goos} GOARCH=${goarch} tags='${tags}' -> ${out#"${REPO_ROOT}/"}"
	if [ "${dry_run}" -eq 1 ]; then
		return 0
	fi

	(
		cd "${REPO_ROOT}"
		CGO_ENABLED=0 GOOS="${goos}" GOARCH="${goarch}" \
			"${GO}" build \
				-trimpath \
				-tags "${tags}" \
				-ldflags "${ldflags}" \
				-o "${out}" \
				"./cmd/${bin}"
	)
}

targets=()
if [ "${use_matrix}" -eq 1 ]; then
	targets=("${matrix_targets[@]}")
else
	host_os="$("${GO}" env GOOS)"
	host_arch="$("${GO}" env GOARCH)"
	if [ -z "${os_list}" ]; then
		os_list="${host_os}"
	fi
	if [ -z "${arch_list}" ]; then
		arch_list="${host_arch}"
	fi
	IFS=',' read -r -a os_array <<< "${os_list}"
	IFS=',' read -r -a arch_array <<< "${arch_list}"
	for o in "${os_array[@]}"; do
		for a in "${arch_array[@]}"; do
			targets+=("${o}/${a}")
		done
	done
fi

failures=0
for tgt in "${targets[@]}"; do
	goos="${tgt%/*}"
	goarch="${tgt#*/}"
	for bin in "${binary_list[@]}"; do
		if ! build_one "${bin}" "${flavor}" "${goos}" "${goarch}"; then
			failures=$((failures + 1))
		fi
	done
done

if [ "${failures}" -ne 0 ]; then
	echo "build.sh: ${failures} build(s) failed" >&2
	exit 1
fi

echo "build.sh: completed successfully"
