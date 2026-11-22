#!/usr/bin/env bash

install_count_directory_entries() {
  local directory=$1
  if [[ ! -d $directory ]]; then
    printf '0\n'
    return 0
  fi
  find "$directory" -mindepth 1 -maxdepth 1 2>/dev/null | wc -l
}
