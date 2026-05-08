#!/usr/bin/env bash
#
# Build cross-platform release archives for y.
#
# Usage:
#   scripts/release.sh [options]
#
# Options:
#   --version <ver>    Version tag for archive names. Default: VERSION env or "dev".
#   --flavor <name>    minimal|standard|full. Default: standard.
#   --tags "<tags>"    Custom tag set; forces flavor=custom.
#   --os <list>        Comma-separated GOOS list. Default: full reference matrix.
#   --arch <list>      Comma-separated GOARCH list. Default: matches OS.
#   --output-dir <p>   Output dir for archives. Default: dist.
#   --bin-dir <p>      Staging dir for built binaries. Default: bin.
#   --skip-build       Reuse existing binaries instead of invoking build.sh.
#   --help             Show this help.
#
# Each archive contains:
#   y[-flavor], y-mom, y-pods (when their flavor compiles them)
#   LICENSE
#   docs/release.md
#   docs/migration-from-pi.md
#
# The script also writes dist/SHA256SUMS covering every produced archive.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
GO="${GO:-go}"

print_help() {
	sed -n '2,/^$/p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
}

reference_targets=(
	"darwin/arm64"
	"darwin/amd64"
	"linux/amd64"
	"linux/arm64"
	"windows/amd64"
)

version="${VERSION:-dev}"
flavor="standard"
custom_tags=""
os_list=""
arch_list=""
output_dir="dist"
bin_dir="bin"
skip_build=0

while [ $# -gt 0 ]; do
	case "$1" in
		--version)
			version="$2"
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
		--output-dir)
			output_dir="$2"
			shift 2
			;;
		--bin-dir)
			bin_dir="$2"
			shift 2
			;;
		--skip-build)
			skip_build=1
			shift
			;;
		--help|-h)
			print_help
			exit 0
			;;
		*)
			echo "release.sh: unknown option: $1" >&2
			exit 2
			;;
	esac
done

dist_dir="${REPO_ROOT}/${output_dir}"
mkdir -p "${dist_dir}"

# Resolve the OS/arch matrix.
targets=()
if [ -n "${os_list}" ] || [ -n "${arch_list}" ]; then
	if [ -z "${os_list}" ] || [ -z "${arch_list}" ]; then
		echo "release.sh: --os and --arch must be set together" >&2
		exit 2
	fi
	IFS=',' read -r -a os_array <<< "${os_list}"
	IFS=',' read -r -a arch_array <<< "${arch_list}"
	for o in "${os_array[@]}"; do
		for a in "${arch_array[@]}"; do
			targets+=("${o}/${a}")
		done
	done
else
	targets=("${reference_targets[@]}")
fi

# Build artefacts for each target unless --skip-build is set.
if [ "${skip_build}" -eq 0 ]; then
	build_args=(--binary all --flavor "${flavor}" --version "${version}" --output-dir "${bin_dir}")
	if [ "${flavor}" = "custom" ]; then
		build_args+=(--tags "${custom_tags}")
	fi
	# Translate the matrix into a single build.sh invocation per target so
	# --tags can flow through unchanged. For the reference matrix we use
	# --matrix; for custom selections we expand explicitly.
	if [ -n "${os_list}" ] || [ -n "${arch_list}" ]; then
		"${SCRIPT_DIR}/build.sh" "${build_args[@]}" --os "${os_list}" --arch "${arch_list}"
	else
		"${SCRIPT_DIR}/build.sh" "${build_args[@]}" --matrix
	fi
fi

# Helper: flavor-aware binary name in bin/.
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

archive_one() {
	local goos="$1"
	local goarch="$2"
	local flv="$3"

	local stage_root
	stage_root="$(mktemp -d "${dist_dir}/.stage.XXXXXX")"
	local pkg_name="y-${version}-${flv}-${goos}-${goarch}"
	local stage="${stage_root}/${pkg_name}"
	mkdir -p "${stage}"

	local found_binary=0
	for bin in y y-mom y-pods; do
		local src="${REPO_ROOT}/${bin_dir}/$(binary_name "${bin}" "${flv}" "${goos}" "${goarch}")"
		if [ ! -f "${src}" ]; then
			continue
		fi
		local dest_name="${bin}"
		if [ "${goos}" = "windows" ]; then
			dest_name="${bin}.exe"
		fi
		cp "${src}" "${stage}/${dest_name}"
		chmod 0755 "${stage}/${dest_name}"
		found_binary=1
	done

	if [ "${found_binary}" -eq 0 ]; then
		rm -rf "${stage_root}"
		echo "release.sh: skipping ${pkg_name} (no binaries found in ${bin_dir})" >&2
		return 0
	fi

	# Copy ancillary files; tolerate missing optional docs.
	for asset in LICENSE docs/release.md docs/migration-from-pi.md; do
		local src="${REPO_ROOT}/${asset}"
		if [ -f "${src}" ]; then
			local dest="${stage}/${asset}"
			mkdir -p "$(dirname "${dest}")"
			cp "${src}" "${dest}"
		fi
	done

	local archive
	if [ "${goos}" = "windows" ]; then
		archive="${dist_dir}/${pkg_name}.zip"
		(cd "${stage_root}" && zip -qr "${archive}" "${pkg_name}")
	else
		archive="${dist_dir}/${pkg_name}.tar.gz"
		# tar's --owner/--group flags differ between BSD and GNU; avoid them
		# for portability and rely on the staging directory permissions.
		(cd "${stage_root}" && tar -czf "${archive}" "${pkg_name}")
	fi

	rm -rf "${stage_root}"
	echo "release.sh: wrote ${archive#"${REPO_ROOT}/"}"
}

flavor_for_archive="${flavor}"
for tgt in "${targets[@]}"; do
	goos="${tgt%/*}"
	goarch="${tgt#*/}"
	archive_one "${goos}" "${goarch}" "${flavor_for_archive}"
done

# SHA256 checksum manifest. We pick whichever shasum tool is on PATH.
shasum_tool=""
if command -v shasum >/dev/null 2>&1; then
	shasum_tool="shasum -a 256"
elif command -v sha256sum >/dev/null 2>&1; then
	shasum_tool="sha256sum"
fi

if [ -n "${shasum_tool}" ]; then
	(
		cd "${dist_dir}"
		# Iterate sorted archive list deterministically.
		mapfile -t files < <(ls -1 *.tar.gz *.zip 2>/dev/null | LC_ALL=C sort)
		if [ "${#files[@]}" -eq 0 ]; then
			echo "release.sh: no archives produced; skipping SHA256SUMS"
			exit 0
		fi
		# shellcheck disable=SC2086
		${shasum_tool} "${files[@]}" > SHA256SUMS
	)
	echo "release.sh: wrote ${output_dir}/SHA256SUMS"
else
	echo "release.sh: WARNING - shasum/sha256sum not found; SHA256SUMS skipped" >&2
fi
