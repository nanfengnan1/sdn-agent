#!/bin/sh
# Fetch into a new build-stage directory; never update a developer's checkout.
set -eu
repo=$1
ref=$2
destination=$3
test -n "$repo" && test -n "$ref" || { printf '%s\n' 'repository and source ref are required' >&2; exit 1; }
case "$ref" in -*) printf '%s\n' 'source ref must not start with -' >&2; exit 1;; esac
mkdir -p "$destination"
test -z "$(ls -A "$destination")" || { printf '%s\n' 'source destination must be empty' >&2; exit 1; }
git init "$destination"
git -C "$destination" remote add origin "$repo"
# Keep tag ancestry: VPP src/scripts/version needs git describe --match v*.
git -C "$destination" fetch --tags origin
git -C "$destination" fetch origin "$ref"
git -C "$destination" checkout --detach FETCH_HEAD
git -C "$destination" submodule update --init --recursive
