#!/bin/sh
set -eu

read_secret() {
	secret_file=$1
	if [ ! -r "$secret_file" ]; then
		echo "Authentik secret is not readable" >&2
		exit 1
	fi
	file_bytes=$(wc -c <"$secret_file")
	secret_value=$(cat -- "$secret_file")
	secret_bytes=$(printf %s "$secret_value" | wc -c)
	framed_bytes=$(printf %s "$secret_value" | tr -d '\r\n' | wc -c)
	if [ -z "$secret_value" ] || [ "$secret_bytes" -gt 4096 ] || [ "$file_bytes" -ne "$secret_bytes" ] || [ "$secret_bytes" -ne "$framed_bytes" ]; then
		echo "Authentik secret has invalid framing" >&2
		exit 1
	fi
	printf %s "$secret_value"
}

read_optional_secret() {
	secret_file=$1
	if [ ! -r "$secret_file" ]; then
		echo "Authentik optional secret is not readable" >&2
		exit 1
	fi
	file_bytes=$(wc -c <"$secret_file")
	secret_value=$(cat -- "$secret_file")
	secret_bytes=$(printf %s "$secret_value" | wc -c)
	framed_bytes=$(printf %s "$secret_value" | tr -d '\r\n' | wc -c)
	if [ "$secret_bytes" -gt 4096 ] || [ "$file_bytes" -ne "$secret_bytes" ] || [ "$secret_bytes" -ne "$framed_bytes" ]; then
		echo "Authentik optional secret has invalid framing" >&2
		exit 1
	fi
	printf %s "$secret_value"
}

AUTHENTIK_POSTGRESQL__PASSWORD=$(read_secret /run/secrets/authentik_postgres_password)
AUTHENTIK_SECRET_KEY=$(read_secret /run/secrets/authentik_secret_key)
AUTHENTIK_EMAIL__PASSWORD=$(read_optional_secret /run/secrets/smtp_password)
export AUTHENTIK_POSTGRESQL__PASSWORD AUTHENTIK_SECRET_KEY AUTHENTIK_EMAIL__PASSWORD

if [ "$#" -eq 1 ] && { [ "$1" = server ] || [ "$1" = worker ]; }; then
	exec dumb-init -- ak "$1"
fi
if [ "$#" -ge 1 ] && [ "$1" = shell ]; then
	exec ak "$@"
fi
echo "Authentik command must be server, worker, or an operator shell" >&2
exit 1
