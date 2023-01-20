#!/bin/sh
# Copyright 2023 Intel Corporation
# SPDX-License-Identifier: Apache-2.0

set -e

# avoid error on missing objects
delete="kubectl delete --ignore-not-found=true"

usage ()
{
	name=${0##*/}
	echo "Apply scalability tester deployments to test cluster"
	echo
	echo "Usage: $name [-v] [test queue names]"
	echo
	echo "All queue deployments are expected to be in directories named as:"
	echo "  deployments/<queue name>-queue/"
	echo
	echo "And their corresponding data volumes (if any), in:"
	echo "  deployments/<queue name>-queue/volume/"
	echo
	echo "All queues, and the frontend serving them, will first be"
	echo "taken down to make sure everything starts from a clean slate."
	echo
	echo "If '-v' option is given, also their volumes are deleted"
	echo "(this is not done by default in case they are shared)."
	echo
	echo "Frontend, deployments for the specified test queues, their"
	echo "data volumes and client control ingress points, are then"
	echo "(re-)applied to the cluster."
	echo
	echo "ERROR: $1!"
	exit 1
}

if [ ! -d deployments ]; then
	usage "'deployments' directory is missing"
fi
cd deployments

del_vols="false"
if [ "$1" = "-v" ]; then
	del_vols="true"
	shift
fi

for name in "$@"; do
	if [ ! -d "$name-queue" ]; then
		usage "'$name-queue' deployment directory for '$name' queue is missing"
	fi
done

echo "Removing all scalability tester deployments..."

for queue in *-queue; do
	name=${queue%-queue}
	echo "Deleting '$name' queue deployment..."
	$delete -f "$queue"
	if [ $del_vols = true ] && [ -d "$queue/volume" ]; then
		echo "Deleting '$name' data volume..."
		$delete -f "$queue/volume/"
	fi
done

echo "Deleting frontend deployment / service..."
$delete -f frontend/
$delete -f frontend/service/

echo
if [ $# -lt 1 ]; then
	echo "No queues specified -> DONE!"
	exit 0
fi

# as these may be used also for other purposes, do not
# remove them, and use apply instead of create
echo "Creating namespaces..."
kubectl apply -f namespace

# everything done in reverse order compared to deletion

echo "Creating frontend..."
kubectl create -f frontend/service/
kubectl create -f frontend/

echo "Starting specified queues..."
for name in "$@"; do
	queue="$name-queue"
	if [ -d "$queue/volume" ]; then
		echo "Creating '$name' data volume..."
		kubectl create -f "$queue/volume/"
	fi
	echo "Create '$name' queue deployment..."
	kubectl create -f "$queue"
done

awk '/name:/{print $2}' namespace/*.yaml | while read -r ns; do
	echo
	echo "Resulting '$ns' namespace deployments:"
	echo "  kubectl -n "$ns" get all | grep -e NAME -e scalability"
	echo
	kubectl -n "$ns" get all | grep -e NAME -e scalability
done

echo
echo "DONE!"
