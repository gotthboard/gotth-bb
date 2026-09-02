#!/bin/sh
set -eu

if [ -n "${DATABASE_URL_FILE:-}" ]; then
	if [ ! -r "$DATABASE_URL_FILE" ]; then
		echo "database secret is not readable" >&2
		exit 1
	fi
	DATABASE_URL=$(cat -- "$DATABASE_URL_FILE")
	if [ -z "$DATABASE_URL" ]; then
		echo "database secret is empty" >&2
		exit 1
	fi
	export DATABASE_URL
	unset DATABASE_URL_FILE
fi

if [ -n "${OIDC_CLIENT_SECRET_FILE:-}" ]; then
	if [ ! -r "$OIDC_CLIENT_SECRET_FILE" ]; then
		echo "OIDC client secret is not readable" >&2
		exit 1
	fi
	OIDC_CLIENT_SECRET=$(cat -- "$OIDC_CLIENT_SECRET_FILE")
	if [ -z "$OIDC_CLIENT_SECRET" ]; then
		echo "OIDC client secret is empty" >&2
		exit 1
	fi
	export OIDC_CLIENT_SECRET
	unset OIDC_CLIENT_SECRET_FILE
fi

if [ "$#" -eq 0 ]; then
	echo "container command is required" >&2
	exit 1
fi

exec "$@"
