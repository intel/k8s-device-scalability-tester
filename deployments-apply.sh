#!/bin/sh
# Copyright 2023 Intel Corporation
# SPDX-License-Identifier: Apache-2.0

set -e

# ---------------- option / arg checking ------------------

SEPARATOR="------------------------------------------------"

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

FRONTEND=frontend/frontend.yaml
NAMESPACE=$(awk '/^ *namespace:/{print $2}' $FRONTEND)
if [ -z "$NAMESPACE" ]; then
	usage "unable to determine '$FRONTEND' namespace"
fi

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

# ---------------- pod state helper ------------------

# wait upto given timeout until specified number of pods with given prefix are in given state
# in given namespace, and none of those pods are in any other state
wait_pod_state ()
{
	SECS=$1
	COUNT=$2
	PREFIX=$3
	SPACE=$4
	STATE=$5

	echo "Waiting ${SECS}s for $COUNT $SPACE/${PREFIX}* deployment(s) to be in '$STATE' state..."

	SLEEP=2
	while [ "$SECS" -gt 0 ]; do
		# start with sleep to make sure terminated pods are not anymore in running state
		SECS=$((SECS-SLEEP))
		sleep $SLEEP

		# show current deployment status, with node names
		kubectl -n "$SPACE" get pods -o wide | grep -F "^$PREFIX" || true

		# requested number of deployments not yet in given state?
		LINES=$(kubectl -n "$SPACE" get pods | grep -c "^${PREFIX}.* $STATE " || true)
		echo "= $LINES/$COUNT pods (${SECS}s remaining)"
		if [ "$LINES" -ne "$COUNT" ]; then
			continue
		fi

		# none in any other state?
		LINES=$(kubectl -n "$SPACE" get pods | grep -F "^$PREFIX" | grep -vcF " $STATE " || true)
		if [ "$LINES" -eq 0 ]; then
			# => wait done
			return
		break
		echo "$LINES pods still in other state"
	fi
	done

	echo "ERROR: deployment wait timed out"

	# on timeout, show log for first erroring pod
	CRASHER=$(kubectl -n "$SPACE" get pods | awk "/^${PREFIX}.* (Crash|Error)/"'{print $1; exit}')
	if [ "$CRASHER" != "" ]; then
		echo $SEPARATOR
		echo "kubectl -n $SPACE logs $CRASHER"
		kubectl -n "$SPACE" logs "$CRASHER"
		echo $SEPARATOR
	fi
	exit 1
}

# ---------------- previous deployment removal ------------------

# avoid error on missing objects
delete="kubectl delete --ignore-not-found=true"

echo "Removing current scalability tester deployments..."

for queue in *-queue; do
	name=${queue%-queue}
	echo "Deleting '$name' queue deployment..."
	$delete -f "$queue"
	# secs count prefix space state
	wait_pod_state 20 0 "scalability-tester-backend-$name-" "$NAMESPACE" "Running"
done

# need to be done after all queues have been deleted,
# in case queues use same data volume
for queue in *-queue; do
	name=${queue%-queue}
	if [ $del_vols = true ] && [ -d "$queue/volume" ]; then
		echo "Deleting '$name' data volume..."
		$delete -f "$queue/volume/"
	fi
done

echo "Deleting frontend deployment / service..."
$delete -f frontend/
$delete -f frontend/service/
# secs count prefix space state
wait_pod_state 20 0 "scalability-tester-frontend-" "$NAMESPACE" "Running"

echo
if [ $# -lt 1 ]; then
	echo "No queues specified -> DONE!"
	exit 0
fi

# ---------------- create specified deployment ------------------

# as these may be used also for other purposes, do not
# remove them, and use apply instead of create
echo "Creating namespaces..."
kubectl apply -f namespace

# everything done in reverse order compared to deletion

echo "Creating frontend..."
kubectl create -f frontend/service/
kubectl create -f frontend/
# secs count prefix space state
wait_pod_state 60 1 "scalability-tester-frontend-" "$NAMESPACE" "Running"

echo "Starting specified queues..."
for name in "$@"; do
	queue="$name-queue"
	if [ -d "$queue/volume" ]; then
		# as these may be shared, use apply, not create
		echo "Creating '$name' data volume..."
		kubectl apply -f "$queue/volume/"
	fi
	echo "Create '$name' queue deployment..."
	kubectl create -f "$queue"
	wait_pod_state 60 1 "scalability-tester-backend-$name-" "$NAMESPACE" "Running"
done

awk '/name:/{print $2}' namespace/*.yaml | while read -r ns; do
	echo
	echo "Resulting '$ns' namespace deployments:"
	echo "  kubectl -n $ns get all | grep -e NAME -e scalability"
	echo
	kubectl -n "$ns" get all | grep -e NAME -e scalability
done

echo
echo "DONE!"
