#!/bin/bash
# lab.jenkins.pipeline-as-code t2: commit the Jenkinsfile.
# Validator (SHELL_ASSERT): `git log --oneline -- Jenkinsfile` exit 0.
set -uo pipefail
mkdir -p ~/repo && cd ~/repo
git rev-parse --is-inside-work-tree >/dev/null 2>&1 || git init -q
git config user.name  >/dev/null 2>&1 || git config user.name  "content-ci"
git config user.email >/dev/null 2>&1 || git config user.email "content-ci@example.dev"
[ -f Jenkinsfile ] || printf 'pipeline {\n  agent any\n  stages {\n    stage("Build") { steps { sh "echo build" } }\n    stage("Test") { steps { sh "echo test" } }\n    stage("Deploy") { steps { sh "echo deploy" } }\n  }\n}\n' > Jenkinsfile
git add Jenkinsfile
git diff --cached --quiet || git commit -q -m "Add declarative Jenkinsfile (pipeline-as-code)"
git log --oneline -- Jenkinsfile | head -1
