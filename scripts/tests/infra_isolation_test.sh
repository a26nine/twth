#!/usr/bin/env bash

# Verify Terraform checks use isolated, backend-free working directories.
# Inputs: None.

set -Eeuo pipefail

readonly TEST_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)
readonly REPOSITORY_ROOT=$(cd "${TEST_DIR}/../.." && pwd -P)

temp_dir=$(mktemp -d)
readonly temp_dir
readonly stub_dir="${temp_dir}/bin"
readonly call_log="${temp_dir}/terraform-calls.log"
mkdir -p "${stub_dir}"
trap 'rm -rf "${temp_dir}"' EXIT

printf '%s\n' \
  '#!/usr/bin/env bash' \
  'printf "%s|%s|%s\n" "${TF_DATA_DIR-unset}" "${AWS_PROFILE-unset}" "$*" >>"${INFRA_CALL_LOG}"' \
  >"${stub_dir}/terraform"
chmod +x "${stub_dir}/terraform"

env \
  PATH="${stub_dir}:${PATH}" \
  INFRA_CALL_LOG="${call_log}" \
  AWS_PROFILE=must-not-leak \
  bash "${REPOSITORY_ROOT}/scripts/utils/test-infra.sh"

calls=()
while IFS= read -r call; do
  calls[${#calls[@]}]=${call}
done <"${call_log}"
[[ ${#calls[@]} -eq 7 ]] || {
  printf 'FAIL: expected 7 Terraform calls, received %d\n' "${#calls[@]}" >&2
  exit 1
}

[[ ${calls[0]} == 'unset|must-not-leak|fmt -check -recursive infra' ]] || {
  printf 'FAIL: unexpected formatting call: %s\n' "${calls[0]}" >&2
  exit 1
}

bootstrap_data_dir=${calls[1]%%|*}
application_data_dir=${calls[4]%%|*}
[[ -n ${bootstrap_data_dir} && ${bootstrap_data_dir} != unset ]] || {
  printf 'FAIL: bootstrap commands did not receive an isolated TF_DATA_DIR\n' >&2
  exit 1
}
[[ -n ${application_data_dir} && ${application_data_dir} != unset ]] || {
  printf 'FAIL: application commands did not receive an isolated TF_DATA_DIR\n' >&2
  exit 1
}
[[ ${bootstrap_data_dir} != "${application_data_dir}" ]] || {
  printf 'FAIL: Terraform roots unexpectedly share TF_DATA_DIR\n' >&2
  exit 1
}

for index in 1 2 3; do
  [[ ${calls[index]%%|*} == "${bootstrap_data_dir}" ]] || {
    printf 'FAIL: bootstrap call %d changed TF_DATA_DIR\n' "${index}" >&2
    exit 1
  }
  [[ ${calls[index]} == "${bootstrap_data_dir}|unset|"* ]] || {
    printf 'FAIL: bootstrap call %d inherited AWS_PROFILE\n' "${index}" >&2
    exit 1
  }
done
for index in 4 5 6; do
  [[ ${calls[index]%%|*} == "${application_data_dir}" ]] || {
    printf 'FAIL: application call %d changed TF_DATA_DIR\n' "${index}" >&2
    exit 1
  }
  [[ ${calls[index]} == "${application_data_dir}|unset|"* ]] || {
    printf 'FAIL: application call %d inherited AWS_PROFILE\n' "${index}" >&2
    exit 1
  }
done

grep -Fq 'init -backend=false -lockfile=readonly -input=false' <<<"${calls[1]}" || {
  printf 'FAIL: bootstrap init is not backend-disabled and lockfile-readonly\n' >&2
  exit 1
}
grep -Fq 'init -backend=false -lockfile=readonly -input=false' <<<"${calls[4]}" || {
  printf 'FAIL: application init is not backend-disabled and lockfile-readonly\n' >&2
  exit 1
}

printf 'PASS: Terraform verification uses isolated data directories\n'
