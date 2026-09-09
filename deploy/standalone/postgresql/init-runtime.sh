#!/bin/sh
set -eu

password_file=/run/secrets/board_postgres_runtime_password
if [ ! -r "$password_file" ]; then
	echo "Board runtime-role password is not readable" >&2
	exit 1
fi
file_bytes=$(wc -c <"$password_file")
GOTTH_BB_RUNTIME_PASSWORD=$(cat -- "$password_file")
password_bytes=$(printf %s "$GOTTH_BB_RUNTIME_PASSWORD" | wc -c)
framed_bytes=$(printf %s "$GOTTH_BB_RUNTIME_PASSWORD" | tr -d '\r\n' | wc -c)
if [ -z "$GOTTH_BB_RUNTIME_PASSWORD" ] || [ "$password_bytes" -gt 1024 ] || [ "$file_bytes" -ne "$password_bytes" ] || [ "$password_bytes" -ne "$framed_bytes" ]; then
	echo "Board runtime-role password has invalid framing" >&2
	exit 1
fi
export GOTTH_BB_RUNTIME_PASSWORD

psql --set ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" <<'SQL'
\getenv runtime_password GOTTH_BB_RUNTIME_PASSWORD
CREATE ROLE gotth_bb_runtime LOGIN PASSWORD :'runtime_password';
SQL

unset GOTTH_BB_RUNTIME_PASSWORD
