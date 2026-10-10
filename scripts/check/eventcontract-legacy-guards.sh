#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

failed=0

legacy_uievent_refs="$(rg -n '\bcontrol\.UIEvent\b' internal --glob '!**/*_test.go' || true)"
if [[ -n "${legacy_uievent_refs}" ]]; then
  echo "Forbidden control.UIEvent references in production code:" >&2
  printf '%s\n' "${legacy_uievent_refs}" >&2
  failed=1
fi

legacy_compat_refs="$(rg -n 'eventcontractcompat' internal --glob '!**/*_test.go' || true)"
if [[ -n "${legacy_compat_refs}" ]]; then
  echo "Forbidden eventcontractcompat references in production code:" >&2
  printf '%s\n' "${legacy_compat_refs}" >&2
  failed=1
fi

legacy_projector_entry_matches="$(rg -n '\bprojector\.Project\(' internal --glob '!**/*_test.go' || true)"
if [[ -n "${legacy_projector_entry_matches}" ]]; then
  echo "Forbidden legacy projector entrypoint usage in production code (use ProjectEvent instead):" >&2
  printf '%s\n' "${legacy_projector_entry_matches}" >&2
  failed=1
fi

followup_heuristic_matches="$(rg -n 'Notice[[:space:]]*!=[[:space:]]*nil|ThreadSelection[[:space:]]*!=[[:space:]]*nil' \
  internal/app/daemon/app_ingress.go \
  internal/core/orchestrator/service_followup_filter.go \
  internal/core/orchestrator/service_path_picker_contract.go \
  internal/core/orchestrator/service_target_picker_owner_card.go \
  --glob '!**/*_test.go' || true)"
if [[ -n "${followup_heuristic_matches}" ]]; then
  echo "Forbidden followup payload heuristics in followup filters (use eventcontract semantics):" >&2
  printf '%s\n' "${followup_heuristic_matches}" >&2
  failed=1
fi

resolver_matches="$(MSYS2_ARG_CONV_EXCL='*' rg -n --path-separator / 'resolveGatewayTarget\(' internal/adapter/feishu --glob '!**/*_test.go' || true)"
resolver_disallowed="$(printf '%s\n' "${resolver_matches}" | \
  grep -vE '^internal/adapter/feishu/(controller_gateway|controller_preview|controller_target_resolver)\.go:' || true)"
if [[ -n "${resolver_disallowed}" ]]; then
  echo "Forbidden resolveGatewayTarget call sites outside controller resolver boundary:" >&2
  printf '%s\n' "${resolver_disallowed}" >&2
  failed=1
fi

if [[ "${failed}" -ne 0 ]]; then
  exit 1
fi
