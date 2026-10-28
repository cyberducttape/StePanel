#!/usr/bin/env bash
# Record the host web-service state before installer package scripts or
# takeover logic can change it. Restore these two operator-visible properties
# if a pre-commit install/upgrade fails; foreign service configuration itself
# is deliberately never overwritten by this helper.
declare -Ag INSTALL_HOST_SERVICE_WAS_ACTIVE=()
declare -Ag INSTALL_HOST_SERVICE_WAS_ENABLED=()

install_capture_service_state() {
  local service=$1 active enabled
  active=$(systemctl is-active "$service" 2>/dev/null || true)
  enabled=$(systemctl is-enabled "$service" 2>/dev/null || true)
  INSTALL_HOST_SERVICE_WAS_ACTIVE[$service]=0
  INSTALL_HOST_SERVICE_WAS_ENABLED[$service]=0
  [[ $active != active ]] || INSTALL_HOST_SERVICE_WAS_ACTIVE[$service]=1
  [[ $enabled != enabled && $enabled != enabled-runtime ]] || INSTALL_HOST_SERVICE_WAS_ENABLED[$service]=1
}

install_restore_service_state() {
  local service=$1 active_now enabled_now wanted_active wanted_enabled
  active_now=$(systemctl is-active "$service" 2>/dev/null || true)
  enabled_now=$(systemctl is-enabled "$service" 2>/dev/null || true)
  wanted_active=${INSTALL_HOST_SERVICE_WAS_ACTIVE[$service]:-0}
  wanted_enabled=${INSTALL_HOST_SERVICE_WAS_ENABLED[$service]:-0}

  if (( wanted_enabled )); then
    if [[ $enabled_now != enabled && $enabled_now != enabled-runtime ]]; then
      systemctl enable "$service" || return
    fi
  elif [[ $enabled_now == enabled || $enabled_now == enabled-runtime ]]; then
    systemctl disable "$service" || return
  fi

  if (( wanted_active )); then
    [[ $active_now == active ]] || systemctl start "$service" || return
  elif [[ $active_now == active ]]; then
    systemctl stop "$service" || return
  fi
}
