#!/usr/bin/env bash
# Adapt an extracted previous-release tree for the upgrade smoke.
#
#   deploy/lab/upgrade-fixture.sh TREE MODE
#
# v0.6.0 and v0.7.0 cannot complete a fresh install as published on a
# current Ubuntu 24.04 host: they run their local database helper through
# sudo, which their own service sandbox (NoNewPrivileges) refuses. The smoke
# needs an installed previous release to upgrade, so:
#
#   helper     drops only the STEPANEL_DBCTL entry. Everything else, including
#              the systemd LoadCredential database-credential handoff the
#              upgrade must migrate, is as shipped. Used on a real guest.
#   container  also rewrites the credential handoff to the service-readable
#              file the current release uses, because the systemd container
#              cannot expose LoadCredential to the service.
set -Eeuo pipefail

tree=${1:?previous release tree is required}
mode=${2:?fixture mode (helper or container) is required}
[[ $mode == helper || $mode == container ]] || { echo "invalid fixture mode: $mode" >&2; exit 1; }
[[ -f $tree/install.sh && -f $tree/deploy/stepanel.service ]] || { echo "not a release tree: $tree" >&2; exit 1; }

# replace FILE SED_EXPRESSION DESCRIPTION applies one edit and fails if it
# changed nothing, so a fixture that no longer matches is reported instead of
# silently testing an unadapted tree.
replace() {
  local file=$1 expression=$2 description=$3 before
  before=$(sha256sum -- "$file")
  sed -i "$expression" "$file"
  [[ $(sha256sum -- "$file") != "$before" ]] || { echo "fixture adaptation did not apply: $description ($file)" >&2; exit 1; }
}

replace "$tree/install.sh" \
  's|^  if \[\[ "\$DB_LOCAL_HELPER" == "1" \]\]; then write_env STEPANEL_DBCTL|  if false; then write_env STEPANEL_DBCTL|' \
  'local database helper entry is not written'
if [[ $mode == container ]]; then
  replace "$tree/deploy/stepanel.service" \
    '/^LoadCredential=db-password:/d; s#Environment=STEPANEL_DB_PASSWORD_FILE=%d/db-password#Environment=STEPANEL_DB_PASSWORD_FILE=/etc/stepanel-db.password#' \
    'service reads the credential file directly'
  replace "$tree/install.sh" \
    's/install -m 0600 -o root -g root "\$INSTALL_TXN\/stepanel-db.password"/install -m 0640 -o root -g "\$APP_USER" "\$INSTALL_TXN\/stepanel-db.password"/' \
    'credential file is readable by the service group'
fi
