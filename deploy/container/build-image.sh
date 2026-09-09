#!/bin/sh
set -eu

fail() {
	echo "build-image: $*" >&2
	exit 1
}

[ "$#" -eq 1 ] || fail "usage: build-image.sh IMAGE_REFERENCE"
image_reference=$1
case "$image_reference" in '' | *[!A-Za-z0-9._:/@-]*) fail "invalid image reference" ;; esac

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
package_root=$(CDPATH= cd -- "$script_dir/../.." && pwd)
release_file=$package_root/RELEASE.txt
[ -f "$release_file" ] && [ ! -L "$release_file" ] || fail "RELEASE.txt is unavailable"

release_value() {
	name=$1
	value=$(sed -n "s/^$name=//p" "$release_file")
	[ -n "$value" ] && [ "$(printf %s "$value" | wc -l)" -eq 0 ] || fail "RELEASE.txt $name is invalid"
	[ "$(grep -c "^$name=" "$release_file")" -eq 1 ] || fail "RELEASE.txt $name is not unique"
	printf %s "$value"
}

version=$(release_value version)
commit=$(release_value commit)
case "$version" in 1.0.0-beta.1 | 1.0.0-beta.1.*) ;; *) fail "release version is not Beta.1" ;; esac
case "$commit" in
	[0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f]) ;;
	*) fail "release commit is invalid" ;;
esac

for binary in gotth-bb gotth-bb-migrate gotth-bb-operator; do
	[ -f "$package_root/$binary" ] && [ ! -L "$package_root/$binary" ] && [ -x "$package_root/$binary" ] || fail "$binary is unavailable"
done
if docker image inspect "$image_reference" >/dev/null 2>&1; then
	fail "image reference already exists"
fi

context=$(mktemp "${TMPDIR:-/tmp}/gotth-bb-image-context.XXXXXX")
trap 'rm -f "$context"' EXIT HUP INT TERM
tar -C "$package_root" -cf "$context" . || fail "build-context archive failed"
docker build --no-cache --network=none \
	--build-arg "GOTTH_BB_VERSION=$version" \
	--build-arg "GOTTH_BB_COMMIT=$commit" \
	--tag "$image_reference" \
	--file deploy/container/Containerfile - <"$context" || fail "image build failed"

for binary in gotth-bb gotth-bb-migrate gotth-bb-operator; do
	package_digest=$(sha256sum "$package_root/$binary" | cut -d ' ' -f 1)
	image_digest=$(docker run --rm --entrypoint sha256sum "$image_reference" "/usr/local/bin/$binary" | cut -d ' ' -f 1)
	[ "$package_digest" = "$image_digest" ] || fail "$binary differs between package and image"
done
expected="gotth-bb version=$version commit=$commit"
for binary in gotth-bb-migrate gotth-bb-operator; do
	identity=$(docker run --rm --entrypoint "/usr/local/bin/$binary" "$image_reference" version)
	[ "$identity" = "$expected" ] || fail "$binary identity differs from RELEASE.txt"
done
label_version=$(docker image inspect "$image_reference" --format '{{ index .Config.Labels "org.opencontainers.image.version" }}')
label_commit=$(docker image inspect "$image_reference" --format '{{ index .Config.Labels "org.opencontainers.image.revision" }}')
[ "$label_version" = "$version" ] && [ "$label_commit" = "$commit" ] || fail "image labels differ from RELEASE.txt"
image_id=$(docker image inspect "$image_reference" --format '{{.Id}}')
case "$image_id" in sha256:[0-9a-f]*) ;; *) fail "image ID is invalid" ;; esac
[ "${#image_id}" -eq 71 ] || fail "image ID is invalid"
echo "build-image: image=$image_reference id=$image_id version=$version commit=$commit result=verified"
