#!/bin/sh
# Copyright 2023 Intel Corporation
# SPDX-License-Identifier: Apache-2.0

# any error terminates
set -e

export http_proxy=

LINE="-----------------------------"

# in milliseconds
FUZZ_DELAY=50
# in seconds, request sleep time
REQ_SLEEP=0.1
# in seconds, for frontend stats
LOG_INTERVAL=2

CLIENT_HTTP="127.0.0.1:9996"

FRONTEND_CLIENT="127.0.0.1:9997"
FRONTEND_HTTP="127.0.0.1:9998"
FRONTEND_WORKER="127.0.0.1:9999"

fpid=0

error_exit () {
	set +e +x

	# kill all fuzzer instances
	killall -q radamsa

	# backend & client die when frontend goes down
	if [ $fpid -gt 0 ]; then
		kill $fpid
	fi
	NAME=${0##*/}
	echo "Fuzz all network inputs in scalability tester."
	echo
	echo "Usage: $NAME <COUNT> <log dir> <tester-frontend> <tester-client> <tester-backend> <fuzz dir>"
	echo
	echo "Log files are written to the current working directory."
	echo
	echo "Arguments are optional, but if given, must be in above order."
	echo "Count is number outputs that each fuzzer instance generates before it exits."
	echo
	echo "Example:"
	echo "  $NAME 9999 logs/ ./tester-frontend-race ./tester-client-race ./tester-backend-msan ./fuzz/"
	echo
	echo "Default:"
	echo "  $NAME 100 logs/ ./tester-frontend ./tester-client ./tester-backend fuzz/"
	echo
	echo "ERROR: $1!"
	exit 1
}

if [ -z "$(which wget)" ]; then
	error_exit "'wget' tool missing"
fi
if [ -z "$(which radamsa)" ]; then
	error_exit "'radamsa' fuzzing tool missing"
fi

COUNT=100
if [ $# -gt 0 ]; then
	COUNT="$1"
	shift
fi
LOGS="logs/"
if [ $# -gt 0 ]; then
	LOGS="$1"
	shift
fi
FRONTEND="./tester-frontend"
if [ $# -gt 0 ]; then
	FRONTEND="$1"
	shift
fi
CLIENT="./tester-client"
if [ $# -gt 0 ]; then
	CLIENT="$1"
	shift
fi
BACKEND="./tester-backend"
if [ $# -gt 0 ]; then
	BACKEND="$1"
	shift
fi
FUZZ="fuzz/"
if [ $# -gt 0 ]; then
	FUZZ="$1"
	shift
fi

if [ "$COUNT" -lt 100 ]; then
	error_exit "Fuzzing should use at least 100 outputs (=5 secs)"
fi
if [ ! -w "$LOGS" ] || [ ! -d "$LOGS" ]; then
	error_exit "'$LOGS' is not writable by tests, or a directory"
fi
if [ ! -x "$FRONTEND" ]; then
	error_exit "'$FRONTEND' (tester-frontend) missing, or not executable"
fi
if [ ! -x "$CLIENT" ]; then
	error_exit "'$CLIENT' (tester-client) missing, or not executable"
fi
if [ ! -x "$BACKEND" ]; then
	error_exit "'$BACKEND' (tester-backend) missing, or not executable"
fi
if [ ! -d "$FUZZ" ]; then
	error_exit "'$FUZZ' is not a directory"
fi

echo "$LINE"

# show everything
#set -x

echo "Run ${FRONTEND##*/} (on background):"
echo "$FRONTEND" \
	-caddr "$FRONTEND_CLIENT" \
	-maddr "$FRONTEND_HTTP" \
	-waddr "$FRONTEND_WORKER" \
	-interval $LOG_INTERVAL \
	sleep fuzz1 fuzz2 \
	"2> $LOGS/frontend.log &"
"$FRONTEND" \
	-caddr "$FRONTEND_CLIENT" \
	-maddr "$FRONTEND_HTTP" \
	-waddr "$FRONTEND_WORKER" \
	-interval $LOG_INTERVAL \
	sleep fuzz1 fuzz2 \
	2> "$LOGS/frontend.log" &
fpid=$!
sleep 1

echo "Run ${CLIENT##*/} doing 2 parallel reqs (on background):"
echo "$CLIENT" \
	-faddr "$FRONTEND_CLIENT" \
	-caddr "$CLIENT_HTTP" \
	-req-max 16 \
	-req-now 2 \
	-name sleep $REQ_SLEEP \
	"2> $LOGS/client.log &"
"$CLIENT" \
	-faddr "$FRONTEND_CLIENT" \
	-caddr "$CLIENT_HTTP" \
	-req-max 16 \
	-req-now 2 \
	-name sleep $REQ_SLEEP \
	2> "$LOGS/client.log" &
cpid=$!

echo "Run ${BACKEND##*/} (on background):"
echo "$BACKEND" \
	-faddr "$FRONTEND_WORKER" \
	-backoff-max $REQ_SLEEP \
	-backoff $REQ_SLEEP \
	sleep $REQ_SLEEP \
	"2> $LOGS/backend.log &"
"$BACKEND" \
	-faddr "$FRONTEND_WORKER" \
	-backoff-max $REQ_SLEEP \
	-backoff $REQ_SLEEP \
	sleep $REQ_SLEEP \
	2> "$LOGS/backend.log" &
bpid=$!

echo "$LINE"

echo "Ask each of the fuzzer instances to generate $COUNT randomized inputs..."

echo "Run fuzzers pretenting to be clients for frontend (on background):"
echo "- radamsa -v -n $COUNT -d $FUZZ_DELAY -o $FRONTEND_CLIENT $FUZZ/client1.json 2> $LOGS/radamsa-client1.log &"
echo "- radamsa -v -n $COUNT -d $FUZZ_DELAY -o $FRONTEND_CLIENT $FUZZ/client2.json 2> $LOGS/radamsa-client2.log &"
radamsa -v -n "$COUNT" -d $FUZZ_DELAY -o "$FRONTEND_CLIENT" "$FUZZ"/client1.json 2> "$LOGS/radamsa-client1.log" &
radamsa -v -n "$COUNT" -d $FUZZ_DELAY -o "$FRONTEND_CLIENT" "$FUZZ"/client2.json 2> "$LOGS/radamsa-client2.log" &

echo "Run fuzzers pretenting to be workers for frontend (on background):"
echo "- radamsa -v -n $COUNT -d $FUZZ_DELAY -o $FRONTEND_WORKER $FUZZ/backend1.json 2> $LOGS/radamsa-backend1.log &"
echo "- radamsa -v -n $COUNT -d $FUZZ_DELAY -o $FRONTEND_WORKER $FUZZ/backend2.json 2> $LOGS/radamsa-backend2.log &"
radamsa -v -n "$COUNT" -d $FUZZ_DELAY -o "$FRONTEND_WORKER" "$FUZZ"/backend1.json 2> "$LOGS/radamsa-backend1.log" &
radamsa -v -n "$COUNT" -d $FUZZ_DELAY -o "$FRONTEND_WORKER" "$FUZZ"/backend2.json 2> "$LOGS/radamsa-backend2.log" &

echo "Run fuzzer doing frontend metric queries (on background):"
echo "- radamsa -v -n $COUNT -d $FUZZ_DELAY -o $FRONTEND_HTTP $FUZZ/metrics.http 2> $LOGS/radamsa-frontend-http.txt &"
radamsa -v -n "$COUNT" -d $FUZZ_DELAY -o "$FRONTEND_HTTP" "$FUZZ"/metrics.http 2> "$LOGS/radamsa-frontend-http.txt" &

echo "Run fuzzer probing all client HTTP endpoints (on background):"
echo "- radamsa -v -n $COUNT -d $FUZZ_DELAY -o $CLIENT_HTTP $FUZZ/client/*.http 2> $LOGS/radamsa-client-http.txt &"
radamsa -v -n "$COUNT" -d $FUZZ_DELAY -o "$CLIENT_HTTP" "$FUZZ"/client/*.http 2> "$LOGS/radamsa-client-http.txt" &

echo "$LINE"

echo "Let fuzzing continue for 5 secs..."
sleep 5

check_fetch () {
	echo "try: wget -O- --no-verbose $*"
	wget -O- --no-verbose "$@"
}

check_http () {
	TEST_URL=$1

	echo "$LINE"
	echo "*** Test longer URL query being blocked ***"
	if check_fetch "$TEST_URL/foobar"; then
		error_exit "longer URL accepted"
	fi

	echo "$LINE"
	echo "*** Test HEAD method being blocked ***"
	if check_fetch --method HEAD "$TEST_URL"; then
		error_exit "HEAD method accepted"
	fi

	echo "$LINE"
	echo "*** Test DELETE method being blocked ***"
	if check_fetch --method DELETE "$TEST_URL"; then
		error_exit "DELETE method accepted"
	fi

	echo "$LINE"
	echo "*** Test invalid (FOOBAR) method being blocked ***"
	if check_fetch --method FOOBAR "$TEST_URL"; then
		error_exit "FOOBAR method accepted"
	fi

	echo "$LINE"
	echo "*** Test POST/BODY query being blocked ***"
	if check_fetch --post-data="user=test" "$TEST_URL"; then
		error_exit "POST method / BODY content accepted"
	fi

	echo "$LINE"
	echo "*** Test normal (GET) query method working ***"
	if ! check_fetch "$TEST_URL"; then
		error_exit "expected fetch failed"
	fi
}

echo "$LINE"

echo "Check that client HTTP endpoint accepts only relevant type of input..."
check_http "$CLIENT_HTTP/fails"

echo "$LINE"

echo "Check that frontend metric endpoint accepts only relevant type of input..."
check_http "$FRONTEND_HTTP/metrics"

echo "$LINE"

# disable any error exits
set +e

echo "Wait for all fuzzers to terminate..."
for fuzzer in %4 %5 %6 %7 %8 %9; do
	echo "- fuzzer: $fuzzer"
	wait $fuzzer
	ret=$?
	if [ $ret -ne 0 ]; then
		error_exit "fuzzer terminated with exit code $ret"
	fi
done

echo "$LINE"

echo "Get frontend metrics:"
wget -O- --no-verbose "$FRONTEND_HTTP/metrics"

echo "Get client statistics:"
wget -O- --no-verbose "$CLIENT_HTTP/stats?type=plain"

echo "$LINE"

echo "Terminating '$BACKEND'..."
if ! kill $bpid; then
	error_exit "killing tester-backend failed"
fi

wait %3
ret=$?
if [ $ret -ne 0 ]; then
	error_exit "tester-backend terminated with exit code $ret"
fi

echo "Terminating '$CLIENT'..."
if ! kill $cpid; then
	error_exit "killing tester-client failed"
fi

wait %2
ret=$?
if [ $ret -ne 0 ]; then
	error_exit "tester-client terminated with exit code $ret"
fi

echo "Terminating '$FRONTEND'..."
if ! kill $fpid; then
	error_exit "killing tester-frontend failed"
fi
fpid=0

wait %1
ret=$?
if [ $ret -ne 0 ]; then
	error_exit "tester-frontend terminated with exit code $ret"
fi

echo "$LINE"
echo "=> SUCCESS!"
