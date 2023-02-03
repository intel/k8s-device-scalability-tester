#!/bin/sh
# Copyright 2023 Intel Corporation
# SPDX-License-Identifier: Apache-2.0

set -e

SEPARATOR="------------------------------------------------"
#TESTHOST="127.0.0.1"
TESTHOST="localhost"
# deployment scale wait
SCALE_SECS=120
# REQ_SECS multiplier
WAIT_COUNT=20
# parallel requests
PARALLEL=-1
TYPE="plain"
#TYPE="json"

usage ()
{
	name=${0##*/}
	echo "Validation script for device scalability in Kubernetes"
	echo
	echo "Usage: $name DEPLOYMENT DEP_SPACE SECS SERVICE SVC_SPACE LIMIT SCALE"
	echo
	echo "Controls backend DEPLOYMENT scaling in DEP_SPACE, and number of parallel requests"
	echo "done to it through client SERVICE in SVC_SPACE.  Verifies that given deployment can"
	echo "be scaled up by doubling its replica count from 1 to LIMIT, with each step improving"
	echo "request throughput (at least) by SCALE (according to requesting SERVICE)."
	echo
	echo "DEPLOYMENT scale-up is waited for ${SCALE_SECS}s (before script fails), and request throughput"
	echo "change data is collected for $WAIT_COUNT*SECS (-> ~$((100/WAIT_COUNT))% error margin). SECS should be equal"
	echo "or longer than the max run time for a single backend workload request."
	echo
	prefix=scalability-tester
	echo "If '$TESTHOST' is given as k8s namespace, default $prefix localhost addresses"
	echo "are used instead of querying Kubernetes."
	echo
	echo "Example(s) for workloads completing in <2 seconds:"
	echo "	$name $prefix-backend-sleep monitoring 2 $prefix-client-sleep validation 16 1.5"
	echo "	$name $prefix-backend-sleep $TESTHOST 2 $prefix-client-sleep $TESTHOST 16 1.5"
	echo
	echo "ERROR: $1!"
	exit 1
}

if [ $# -ne 7 ]; then
	usage "incorrect number of arguments"
fi

DEPLOYMENT=$1
DEP_SPACE=$2
# max workload run request completion time
REQ_SECS=$3
SERVICE=$4
SVC_SPACE=$5
LIMIT=$6
SCALE=$7

# check for helpers
if [ -z "$(which curl)" ]; then
	echo "ERROR: 'curl' missing"
	exit 1
fi
CURL="curl --no-progress-meter"
if [ $TYPE = "json" ] && [ -z "$(which jq)" ]; then
	echo "ERROR: 'jq' missing"
	exit 1
fi

echo "Current contents of the client service namespace:"
echo $SEPARATOR
kubectl -n "$SVC_SPACE" get all -o wide
echo $SEPARATOR

# service URLs
if [ "$SVC_SPACE" = "$TESTHOST" ]; then
	SVC_URL="$TESTHOST:9996"
else
	SVC_URL=$(kubectl -n "$SVC_SPACE" get svc "$SERVICE" --no-headers=true |\
	  sed 's%/TCP%%' | awk '{printf("%s:%s", $3, $5)}')
fi
if [ -z "$SVC_URL" ]; then
	echo "ERROR: failed to get '$SERVICE' service endpoint"
	exit 1
fi
echo "=> client service URL = $SVC_URL"

URL_PARALLEL="$SVC_URL/parallel?type=$TYPE&value"
URL_THROUGHPUT="$SVC_URL/reqs-per-sec?type=$TYPE"
URL_FAILS="$SVC_URL/fails?type=$TYPE"
URL_STATS="$SVC_URL/stats?type=$TYPE"
URL_RESET="$SVC_URL/reset?type=$TYPE"
URL_NODES="$SVC_URL/nodes"

cleanup ()
{
	echo
	echo $SEPARATOR
	echo "Tempfile content: '$(cat "$TMPFILE")'"
	echo $SEPARATOR
	rm -r "$TMPDIR"
}

TMPDIR=$(mktemp --directory --suff -scale-validation)
echo "Directory for caching client responses:"
ls -ld "$TMPDIR"
echo

TMPFILE=$TMPDIR/output
trap cleanup EXIT

get_typed ()
{
	if [ $TYPE = "plain" ]; then
		cat "$1"
	elif [ $TYPE = "json" ]; then
		jq .result "$1"
	else
		echo "ERROR: unrecognized type '$TYPE'"
		exit 1
	fi
}

set_parallel ()
{
	if [ "$PARALLEL" -eq "$1" ]; then
		return
	fi
	PARALLEL=$1
	if [ "$PARALLEL" -eq 0 ]; then
		echo "Disabling client requests..."
	else
		echo "Setting number of parallel client requests to $PARALLEL..."
	fi
	echo "$CURL \"$URL_PARALLEL=$PARALLEL\" > $TMPFILE"
	$CURL "$URL_PARALLEL=$PARALLEL" > "$TMPFILE"
	ret=$(get_typed "$TMPFILE")
	if [ "$ret" != "$1" ]; then
		echo "ERROR: $ret != $1"
		exit 1
	fi
}

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

scale_backends_up ()
{
	set_parallel 0

	COUNT=$1
	cmd="kubectl scale -n $DEP_SPACE deployment/$DEPLOYMENT --replicas=$COUNT"
	if [ "$DEP_SPACE" != "$TESTHOST" ]; then
		echo "Scaling backend deployments up to $COUNT..."
		echo "$cmd"
		$cmd
	fi

	# remaining scale wait: wait_pod_state() -> SECS
	wait_pod_state "$SCALE_SECS" "$COUNT" "${DEPLOYMENT}-" "$DEP_SPACE" "Running"

	# remaining request wait = REQ_SECS - (SCALE_SECS - SECS)
	SECS=$((REQ_SECS-SCALE_SECS+SECS))
	if [ $SECS -gt 0 ]; then
		echo "Waiting extra ${SECS}s to make sure previous requests have finished..."
		sleep $SECS
	fi
}

scale_backends_down ()
{
	set_parallel 0

	COUNT=$1
	cmd="kubectl scale -n $DEP_SPACE deployment/$DEPLOYMENT --replicas=$COUNT"
	if [ "$DEP_SPACE" != "$TESTHOST" ]; then
		echo "Scaling backend deployments down to $COUNT..."
		echo "$cmd"
		$cmd
	fi
	wait_pod_state "$SCALE_SECS" "$COUNT" "${DEPLOYMENT}-" "$DEP_SPACE" "Running"
}


echo "Verifying that backend deployment can be scaled to maximum..."
scale_backends_up "$LIMIT"

echo $SEPARATOR

echo "Discarding all backend deployments..."
scale_backends_down 0

OLD="0"
FAIL=0
COUNT=1

WAIT_SECS=$((WAIT_COUNT*REQ_SECS))

while [ $COUNT -le "$LIMIT" ]; do
	echo $SEPARATOR

	echo "Scaling deployments up to $COUNT..."
	scale_backends_up "$COUNT"

	echo "Reseting client metrics..."
	echo "$CURL \"$URL_RESET\" > $TMPFILE"
	$CURL "$URL_RESET" > "$TMPFILE"
	ret=$(get_typed "$TMPFILE")
	if [ "$ret" != "Reseted" ]; then
		echo "ERROR: metrics reset failed"
		exit 1
	fi

	echo "Setting client request parallelization also to $COUNT..."
	set_parallel "$COUNT"
	CUR_COUNT=$COUNT
	COUNT=$((2*COUNT))

	echo "Waiting ${WAIT_SECS}s for throughput metrics collection..."
	sleep "$WAIT_SECS"

	echo "Fetching throughput..."
	echo "$CURL \"$URL_THROUGHPUT\" > $TMPFILE"
	$CURL "$URL_THROUGHPUT" > "$TMPFILE"
	RPS=$(get_typed "$TMPFILE")
	if [ -z "$RPS" ]; then
		echo "ERROR: request-per-second fetch failed"
		exit 1
	fi

	echo "Fetching failure count..."
	echo "$CURL \"$URL_FAILS\" > $TMPFILE"
	$CURL "$URL_FAILS" > "$TMPFILE"
	FAILED=$(get_typed "$TMPFILE")
	if [ -z "$FAILED" ]; then
		echo "ERROR: failure count fetch failed"
		exit 1
	fi

	echo $SEPARATOR
	echo "$CURL \"$URL_STATS\""
	$CURL "$URL_STATS"

	if [ "$FAILED" -gt 0 ]; then
		echo "FAIL: $FAILED request failure(s)"
		FAIL=$((FAIL+1))
	fi

	if [ -z "$(echo "$RPS" | tr -d .0)" ]; then
		# new result was zero
		echo "FAIL: throughput was zero!"
		FAIL=$((FAIL+1))
		OLD=$RPS
		continue
	fi
	if [ -z "$(echo "$OLD" | tr -d .0)" ]; then
		# old result was zero
		echo "Throughput: $RPS reqs-per-sec (pods: $CUR_COUNT)"
		OLD=$RPS
		continue
	fi

	echo $SEPARATOR
	CHANGE=$(echo "scale=2; $RPS/$OLD" | bc)
	echo "Throughput: $OLD -> $RPS = $CHANGE increase on reqs-per-sec (pods: $CUR_COUNT)"
	OLD=$RPS

	if [ "$(echo "$CHANGE / $SCALE" | bc)" -lt 1 ]; then
		echo $SEPARATOR
		echo "FAIL: change $CHANGE < $SCALE"
		echo $SEPARATOR
		echo "$CURL \"$URL_NODES\""
		$CURL "$URL_NODES"
		FAIL=$((FAIL+1))
	fi
done

# show node/device spread
echo $SEPARATOR
echo "$CURL \"$URL_NODES\""
$CURL "$URL_NODES"
echo $SEPARATOR

# disable load & extra log generation
set_parallel 0

# success -> disable trap
rm -r "$TMPDIR"
trap - EXIT

if [ $FAIL -gt 0 ]; then
	echo "*** $FAIL FAILs ***"
	echo "After debugging the issues, scale backends down with:"
	echo "kubectl scale -n $DEP_SPACE deployment/$DEPLOYMENT --replicas=1"
	exit $FAIL
fi

# scale backends down on success
scale_backends_down 1
echo "*** PASS ***"
