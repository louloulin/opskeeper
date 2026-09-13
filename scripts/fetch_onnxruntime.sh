#!/usr/bin/env bash
set -euo pipefail

version="${1:-1.20.1}"
requested_arch="${2:?target architecture is required, expected amd64 or arm64}"
mirror="${3:-https://github.com/microsoft/onnxruntime/releases/download/v${version}}"

case "${requested_arch}" in
  amd64|x86_64)
    target_arch=amd64
    package_arch=x64
    runtime_arch=x64
    expected_sha256=2c2db5b07f4593bbfebea0aef97b4d2e3a3f6611714ae6945b389938fd5758d2
    ;;
  arm64|aarch64)
    target_arch=arm64
    package_arch=aarch64
    runtime_arch=aarch64
    expected_sha256=25dfef4ee35d869a11838ddee5d8d1e5f50155725347e5fbae60608f81ff6112
    ;;
  *)
    echo "unsupported target architecture: ${requested_arch}" >&2
    exit 2
    ;;
esac

cache_dir=builder-cache
cache="${cache_dir}/onnxruntime-linux-${target_arch}-${version}.tgz"
expected_member="onnxruntime-linux-${runtime_arch}-${version}/lib/libonnxruntime.so.${version}"

validate_archive() {
  local archive="$1"
  local actual_sha256
  gzip -t "${archive}"
  if command -v sha256sum >/dev/null 2>&1; then
    actual_sha256="$(sha256sum "${archive}" | awk '{print $1}')"
  else
    actual_sha256="$(shasum -a 256 "${archive}" | awk '{print $1}')"
  fi
  if [[ "${actual_sha256}" != "${expected_sha256}" ]]; then
    echo "ONNX Runtime checksum mismatch for ${archive}" >&2
    exit 1
  fi
  tar -tzf "${archive}" | grep -qx "${expected_member}"
}

if [[ -f "${cache}" ]] && validate_archive "${cache}"; then
  echo "ONNX Runtime ${version} linux/${target_arch} cache is ready: ${cache}"
  exit 0
fi

mkdir -p "${cache_dir}"
download="${cache_dir}/.onnxruntime-linux-${target_arch}-${version}.tmp"
trap 'rm -f "${download}"' EXIT

curl \
  --fail \
  --location \
  --retry 5 \
  --retry-delay 2 \
  --retry-all-errors \
  --output "${download}" \
  "${mirror}/onnxruntime-linux-${package_arch}-${version}.tgz"

validate_archive "${download}"
mv "${download}" "${cache}"
trap - EXIT

echo "fetched ONNX Runtime ${version} linux/${target_arch} to ${cache}"
