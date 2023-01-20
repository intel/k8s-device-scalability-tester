#!/bin/sh
# Copyright 2023 Intel Corporation
# SPDX-License-Identifier: Apache-2.0

set -e

REGISTRY="registry/intel"
PROM_NS="monitoring"
TEST_NS="validation"

usage ()
{
	name=${0##*/}
	echo "Configure scalability tester deployments to test cluster"
	echo
	echo "Usage: $name REGISTRY PROM_NS TEST_NS"
	echo
	echo "Script changes following items for the deployments:"
	echo "- REGISTRY: registry/project for container images"
	echo "- PROM_NS: namespace where deployments providing Prometheus metrics should run"
	echo "- TEST_NS: namespace where rest of the deployments should run"
	echo
	echo "(And all the references to those namespaces.)"
	echo
	echo "Finally it lists the resulting image URLs and deployment namespaces."
	echo
	echo "Example setting things to default values:"
	echo "	$name $REGISTRY $PROM_NS $TEST_NS"
	echo
	echo "ERROR: $1!"
	exit 1
}

if [ ! -d .git ]; then
	usage "this should be run from the project git directory root"
fi

if [ ! -d deployments ]; then
	usage "'deployments' directory is missing"
fi
cd deployments

if [ $# -ne 3 ]; then
	usage "incorrect number of arguments"
fi

for i in "$@"; do
	invalid=$(echo "$i" | tr -d a-zA-Z0-9/_.-)
	if [ -n "$invalid" ]; then
		usage "'$i' include invalid chars ('$invalid') for a valid namespace or registry name"
	fi
done

REGISTRY=$1
PROM_NS=$2
TEST_NS=$3

echo "Reverting deployments back to their original state for NS matching..."
git checkout .
echo

# Point deployment images to correct image registry / project
git ls-files -z '*.yaml' | xargs -0 sed -i "s%image:.*/%image: $REGISTRY/%"

echo "Resulting image URLs:"
git grep image: '*.yaml'
echo

# Change used Prometheus monitoring namespace
# (while making sure apiVersion does not get changed)
git ls-files -z '*.yaml' | xargs -0 sed -i \
  -e "s/monitoring.coreos.com/MONITORING.coreos.com/" \
  -e "s/monitoring/$PROM_NS/" \
  -e "s/MONITORING.coreos.com/monitoring.coreos.com/"

# Change namespace for rest of tester deployments
git ls-files -z '*.yaml' | xargs -0 sed -i "s/validation/$TEST_NS/"

echo "All used namespaces:"
git grep -h 'namespace: ' '*.yaml' | sed 's/^[^:]*:/-/' | sort -u

echo
echo "'git diff' shows the changes."
echo
echo "To revert back to original state, use 'git checkout deployments/'."
