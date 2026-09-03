#!/bin/bash
# lab.gitlab.cicd-pipelines t1: ~/repo/.gitlab-ci.yml with
# stages [build,test,deploy] and one job per stage. Bootstraps ~/repo.
set -uo pipefail
mkdir -p ~/repo && cd ~/repo
git rev-parse --is-inside-work-tree >/dev/null 2>&1 || git init -q
cat > .gitlab-ci.yml <<'YML'
stages:
  - build
  - test
  - deploy

build-job:
  stage: build
  script:
    - echo "compiling"

test-job:
  stage: test
  script:
    - echo "running tests"

deploy-job:
  stage: deploy
  script:
    - echo "deploying"
YML
echo ".gitlab-ci.yml written"
