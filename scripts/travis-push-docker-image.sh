#!/usr/bin/env bash
# exit immediately when a command fails
set -e
# only exit with zero if all commands of the pipeline exit successfully
set -o pipefail
# error on unset variables
set -u

export REPO="logancloud/logan-app-operator"

if [[ "${TRAVIS_PULL_REQUEST}" = "false" ]]; then
    docker login -u "$DOCKER_USERNAME" -p "$DOCKER_PASSWORD"
    docker push ${IMG}
fi

if [[ "${TRAVIS_TAG}" != "" ]]; then
	# For both git tags and git branches 'TRAVIS_BRANCH' contains the name.
    export TAG="${TRAVIS_BRANCH}"
    docker tag ${REPO}:latest "${REPO}:${TAG}"
    docker login -u "$DOCKER_USERNAME" -p "$DOCKER_PASSWORD"
    echo "Pushing to docker hub ${REPO}:${TAG}"
    docker push "${REPO}:${TAG}"
fi
