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
    docker push ${REPO}:latest
fi

pull_requst_id=""
git_logs=$(git log -1)
re="Merge pull request #([0-9]+) .*"
if [[ $git_logs =~ $re ]]; then
    pull_requst_id=${BASH_REMATCH[1]}
fi

echo "pull_requst_id: ${pull_requst_id}"
if [[ "${pull_requst_id}" != "" ]] && [[ "${TRAVIS_TAG}" == "" ]]; then
    export TAG="latest-PR${pull_requst_id}"
    docker tag ${REPO}:latest "${REPO}:${TAG}"
    docker images
    docker login -u "$DOCKER_USERNAME" -p "$DOCKER_PASSWORD"
    echo "Pushing to docker hub ${REPO}:${TAG}"
    docker push "${REPO}:${TAG}"
fi

if [[ "${TRAVIS_TAG}" != "" ]]; then
	# For both git tags and git branches 'TRAVIS_BRANCH' contains the name.
    export TAG="${TRAVIS_BRANCH}"
    docker tag ${REPO}:latest "${REPO}:${TAG}"
    docker login -u "$DOCKER_USERNAME" -p "$DOCKER_PASSWORD"
    echo "Pushing to docker hub ${REPO}:${TAG}"
    docker push "${REPO}:${TAG}"
fi
