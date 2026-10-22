#!/bin/sh
set -eu
forgejo_remote=${GOTTH_FORGEJO_REMOTE:-origin}
github_api=${GITHUB_API_ROOT:-https://api.github.com}
github_web=${GITHUB_WEB_ROOT:-https://github.com}
test_mode=${CLOSEOUT_TEST_MODE:-0}
if [ "$test_mode" != 1 ]; then
 [ "$github_api" = https://api.github.com ] && [ "$github_web" = https://github.com ] || { echo "noncanonical GitHub origin rejected" >&2; exit 1; }
fi
expected=${EXPECT_STATE:-incomplete}
repo=gotthboard/gotth-bb
beta_tag=1.0.0-beta.1.9
archive=gotth-bb-1.0.0-beta.1.9-linux-amd64.tar.gz
archive_sha=a6c611e1f3d402fe4eab460fda7864eb14669f2a98e628cde0884d724ce99690
sums_sha=3d70330095e1ceb79940b3e9ea2665135d6c3bc5d5389d3f6524bc485bbd4df2
case "$expected" in incomplete|complete) ;; *) echo "EXPECT_STATE must be incomplete or complete" >&2; exit 2 ;; esac
if url=$(git remote get-url "$forgejo_remote" 2>/dev/null); then :; else url=$forgejo_remote; fi
case "$url" in forgejo:gotthboard/gotth-bb.git|ssh://forgejo@git.dannyhunn.com:56087/gotthboard/gotth-bb.git|https://git.dannyhunn.com/gotthboard/gotth-bb.git) ;; *) echo "GOTTH_FORGEJO_REMOTE is not canonical Forgejo" >&2; exit 1 ;; esac
identity() {
 case "$1" in
  1.0.0-alpha.1) echo ee2ac56abcd0b4e3f1f9cdfde8fbf93a93f60937 0d2150e06d25f92e88c2492a314a279c04d7b5af ;;
  1.0.0-alpha.2) echo 43e8e888ba14646feb726839c86762364a8ddb09 47c3a17d8b147e648ad8c57fbc80cec06076e89b ;;
  1.0.0-beta.1) echo cb44076961c525b3a82641c0537b8c6849b53bd3 96ac77b2d9fad03bdd528b83a2c0763820ed644c ;;
  1.0.0-beta.1.1) echo 14bf06bf4186f6b59560972e8fe1eb8598921ea0 e66324e9e5c5d656807da5e9039c1ff4b55626fa ;;
  1.0.0-beta.1.2) echo 6030741b6004d774df390814aed02bd394c99f4c 93b3394aa7222f3e19fd89736385fffabc6afd13 ;;
  1.0.0-beta.1.3) echo e576f59dcf994df3a5e6f547e2848789e88a286e 2db6432f51142520a3750f0edbd69e33d7a680bf ;;
  1.0.0-beta.1.4) echo 90f87e7d55d4b64d6dcc65bc3b1e6562d56cc1ca 67d68f4131bf892b719d60c2128d98d5b6c2509b ;;
  1.0.0-beta.1.5) echo d7b548cfd3f964160b299106b2376aa89fbaa13c 9211cb54b8d689594466493170a997b48b4b1b54 ;;
  1.0.0-beta.1.6) echo 45a7f73c556886d98fb9613386d970d7d97b1ea2 0298910681485a3ea04d1e74cb683e11b7bb4e1b ;;
  1.0.0-beta.1.7) echo 77c84e2938501d3edcdd2ad18390310dd6ce7792 5d398167ef7d9e11a48f9ef083d4abd3cc44c4f4 ;;
  1.0.0-beta.1.8) echo 488d4ead2b584248d6c34938eaaed5823593321f 220e53d0ecd5bef4ca427e4136c53edc9d698c99 ;;
  1.0.0-beta.1.9) echo a9e59395a9c522f92c1594364456a66c5cd6f202 ba23abd4c3bc825efafd31d56c671c15a31bf902 ;;
 esac
}
status_marker() {
 case "$1" in
  1.0.0-alpha.1|1.0.0-alpha.2) echo historical-alpha ;;
  1.0.0-beta.1) echo failed-search-overflow ;;
  1.0.0-beta.1.1) echo failed-breadcrumb-accessibility ;;
  1.0.0-beta.1.2) echo rejected-auth-topology ;;
  1.0.0-beta.1.3) echo superseded-footer-defect ;;
  1.0.0-beta.1.4) echo superseded-area-ui-defect ;;
  1.0.0-beta.1.5) echo owner-acceptance-unrecorded ;;
  1.0.0-beta.1.6) echo failed-register-404 ;;
  1.0.0-beta.1.7) echo failed-smtp-administration ;;
  1.0.0-beta.1.8) echo failed-restricted-role-publication ;;
  1.0.0-beta.1.9) echo candidate-owner-acceptance-pending ;;
 esac
}
github_release() { out=$1; url=$2; if [ -n "${GITHUB_TOKEN:-}" ]; then curl -sS -L -o "$out" -w '%{http_code}' -H "Authorization: Bearer $GITHUB_TOKEN" -H 'Accept: application/vnd.github+json' -H 'User-Agent: gotth-release-closeout' "$url"; else curl -sS -L -o "$out" -w '%{http_code}' -H 'Accept: application/vnd.github+json' -H 'User-Agent: gotth-release-closeout' "$url"; fi; }
tags='1.0.0-alpha.1 1.0.0-alpha.2 1.0.0-beta.1 1.0.0-beta.1.1 1.0.0-beta.1.2 1.0.0-beta.1.3 1.0.0-beta.1.4 1.0.0-beta.1.5 1.0.0-beta.1.6 1.0.0-beta.1.7 1.0.0-beta.1.8 1.0.0-beta.1.9'
root=$(mktemp -d); trap 'rm -rf "$root"' EXIT HUP INT TERM
for tag in $tags; do
 set -- $(identity "$tag"); want_object=$1; want_commit=$2; marker=$(status_marker "$tag")
 [ "$(git cat-file -t "refs/tags/$tag")" = tag ] || { echo "$tag is not annotated" >&2; exit 1; }
 [ "$(git rev-parse "refs/tags/$tag")" = "$want_object" ] && [ "$(git rev-parse "refs/tags/$tag^{}")" = "$want_commit" ] || { echo "$tag local identity mismatch" >&2; exit 1; }
 for remote in "$forgejo_remote" https://github.com/$repo.git; do refs=$(git ls-remote "$remote" "refs/tags/$tag" "refs/tags/$tag^{}"); [ "$(printf '%s
' "$refs"|awk -v r="refs/tags/$tag" '$2==r{print $1}')" = "$want_object" ] && [ "$(printf '%s
' "$refs"|awk -v r="refs/tags/$tag^{}" '$2==r{print $1}')" = "$want_commit" ] || { echo "$tag $remote mismatch" >&2; exit 1; }; done
 body="$root/$tag.json"; code=$(github_release "$body" "$github_api/repos/$repo/releases/tags/$tag")
 if [ "$expected" = incomplete ]; then [ "$code" = 404 ] || { echo "$tag GitHub Release state changed: HTTP $code" >&2; exit 1; }; continue; fi
 [ "$code" = 200 ] || { echo "$tag GitHub Release missing: HTTP $code" >&2; exit 1; }
 if [ "$tag" = "$beta_tag" ]; then
  python3 -c 'import json,sys; d=json.load(open(sys.argv[1])); n=[a.get("name") for a in d.get("assets",[])]; marker="GOTTH-Release-Status: "+sys.argv[4]; ok=d.get("tag_name")==sys.argv[2] and d.get("draft") is False and d.get("prerelease") is True and marker in d.get("body","").splitlines() and len(n)==2 and set(n)=={sys.argv[3],"SHA256SUMS"}; sys.exit(0 if ok else 1)' "$body" "$tag" "$archive" "$marker"
  archive_url=$(python3 -c 'import json,sys; d=json.load(open(sys.argv[1])); print({a["name"]:a["browser_download_url"] for a in d["assets"]}[sys.argv[2]])' "$body" "$archive"); sums_url=$(python3 -c 'import json,sys; d=json.load(open(sys.argv[1])); print({a["name"]:a["browser_download_url"] for a in d["assets"]}[sys.argv[2]])' "$body" SHA256SUMS)
  case "$archive_url" in "$github_web/$repo/releases/download/$tag/$archive") ;; *) echo "unsafe archive URL" >&2; exit 1 ;; esac; case "$sums_url" in "$github_web/$repo/releases/download/$tag/SHA256SUMS") ;; *) echo "unsafe checksum URL" >&2; exit 1 ;; esac
  curl -fsSL -o "$root/$archive" "$archive_url"; curl -fsSL -o "$root/SHA256SUMS" "$sums_url"; printf '%s  %s
' "$archive_sha" "$root/$archive"|sha256sum -c -; printf '%s  %s
' "$sums_sha" "$root/SHA256SUMS"|sha256sum -c -; (cd "$root" && sha256sum -c SHA256SUMS)
 else
  python3 -c 'import json,sys; d=json.load(open(sys.argv[1])); marker="GOTTH-Release-Status: "+sys.argv[3]; ok=d.get("tag_name")==sys.argv[2] and d.get("draft") is False and d.get("prerelease") is True and marker in d.get("body","").splitlines() and len(d.get("assets",[]))==0; sys.exit(0 if ok else 1)' "$body" "$tag" "$marker"
 fi
done
printf 'published_releases=%s draft_state=%s beta119_assets=%s
' "$([ "$expected" = complete ] && echo 12 || echo 0)" "$([ "$expected" = complete ] && echo published || echo unknown)" "$([ "$expected" = complete ] && echo verified || echo absent)"
