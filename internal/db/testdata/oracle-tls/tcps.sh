#!/bin/bash
# Runs on each start of compose.yaml's oracle service, before it reports
# healthy: TCPS on port 2484, beside TCP on 1521, and a second listener on
# 2485 whose TCPS asks for a client certificate, both with the wallet
# TestWriteOracleTLS writes (mounted at /opt/oracle/tls). The image has no
# orapki, which needs Java.
#
# The listener and each server process check client certificates on their
# own, by listener.ora and sqlnet.ora, so the second listener runs with a
# TNS_ADMIN of its own. Run in a subshell: the image sources a script that
# is not executable.
(
  set -eu
  admin="$ORACLE_HOME/network/admin"
  wallet='WALLET_LOCATION = (SOURCE = (METHOD = FILE)(METHOD_DATA = (DIRECTORY = /opt/oracle/tls)))'
  # listener.ora and sqlnet.ora are links into the data files.
  if ! grep -q 'PORT = 2484' "$admin/listener.ora"; then
    sed -i --follow-symlinks 's|(ADDRESS = (PROTOCOL = TCP)(HOST=)(PORT = 1521))|&\n      (ADDRESS = (PROTOCOL = TCPS)(HOST=)(PORT = 2484))|' "$admin/listener.ora"
    grep -q 'PORT = 2484' "$admin/listener.ora"
  fi
  for f in listener.ora sqlnet.ora; do
    if ! grep -qF "$wallet" "$admin/$f"; then
      printf '%s\nSSL_CLIENT_AUTHENTICATION = FALSE\n' "$wallet" >> "$admin/$f"
    fi
  done
  mtls=/tmp/dgopher-mtls
  mkdir -p "$mtls"
  cat > "$mtls/listener.ora" <<EOF
LISTENER_MTLS =
  (DESCRIPTION_LIST =
    (DESCRIPTION =
      (ADDRESS = (PROTOCOL = TCPS)(HOST=)(PORT = 2485))
    )
  )
SID_LIST_LISTENER_MTLS =
  (SID_LIST =
    (SID_DESC = (GLOBAL_DBNAME = FREEPDB1)(SID_NAME = $ORACLE_SID)(ORACLE_HOME = $ORACLE_HOME))
  )
$wallet
SSL_CLIENT_AUTHENTICATION = TRUE
EOF
  grep -v SSL_CLIENT_AUTHENTICATION "$admin/sqlnet.ora" > "$mtls/sqlnet.ora"
  echo 'SSL_CLIENT_AUTHENTICATION = TRUE' >> "$mtls/sqlnet.ora"
  lsnrctl stop > /dev/null
  lsnrctl start > /dev/null
  TNS_ADMIN="$mtls" lsnrctl start LISTENER_MTLS > /dev/null
  printf 'WHENEVER SQLERROR EXIT FAILURE\nALTER SYSTEM REGISTER;\n' | sqlplus -s / as sysdba > /dev/null
  echo "TCPS on 2484, and with client certificates on 2485"
)
