#!/bin/sh
# Copyright 2023 Intel Corporation
# SPDX-License-Identifier: Apache-2.0

set -e

usage ()
{
	name=${0##*/}
	echo "Create new queue deployments from an existing queue"
	echo
	echo "Usage: $name OLD NEW"
	echo
	echo "Script copies OLD backend queue deployments and renames its"
	echo "files and k8s object names to match NEW queue name."
	echo
	echo "Example:"
	echo "	$name media 4k-avc-2-hevc"
	echo
	echo "ERROR: $1!"
	exit 1
}

if [ ! -d deployments ]; then
	usage "'deployments' directory is missing"
fi
cd deployments

if [ $# -ne 2 ]; then
	usage "incorrect number of arguments"
fi

invalid=$(echo "$2" | tr -d a-zA-Z0-9/_.-)
if [ -n "$invalid" ]; then
	usage "'$2' include invalid chars ('$invalid') for k8s object name"
fi

OLD="$1"
NEW="$2"
OLD_Q="$OLD-queue"
NEW_Q="$NEW-queue"

if [ ! -d "$OLD_Q" ]; then
	usage "'$OLD_Q/' deployment directory missing"
fi

# new dir + volume
mkdir -p "$NEW_Q"
if [ -d "$OLD_Q/volume" ]; then
	echo "copying: $OLD_Q/volume/ -> $NEW_Q/volume/"
	cp -a "$OLD_Q/volume" "$NEW_Q/volume"
fi

# copy files with renaming
find "$OLD_Q" -maxdepth 1 -name '*.yaml' | while read -r OLD_F; do
	NEW_F=$(echo "$OLD_F" | sed "s/$OLD/$NEW/g")
	echo "converting: $OLD_F -> $NEW_F"
	sed "s/$OLD/$NEW/g" < "$OLD_F" > "$NEW_F"
done
