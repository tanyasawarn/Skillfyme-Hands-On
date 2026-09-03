#!/bin/bash
# lab.jenkins.pipeline-as-code t1: declarative Jenkinsfile in ~/repo,
# stages Build -> Test -> Deploy in order. fx.git-repo-empty.v1 has no
# handler so this bootstraps ~/repo. Idempotent: overwrite Jenkinsfile.
set -uo pipefail
mkdir -p ~/repo && cd ~/repo
git rev-parse --is-inside-work-tree >/dev/null 2>&1 || git init -q
git config user.name  >/dev/null 2>&1 || git config user.name  "content-ci"
git config user.email >/dev/null 2>&1 || git config user.email "content-ci@example.dev"
cat > Jenkinsfile <<'JF'
pipeline {
    agent any
    stages {
        stage('Build') {
            steps { sh 'echo building' }
        }
        stage('Test') {
            steps { sh 'echo testing' }
        }
        stage('Deploy') {
            steps { sh 'echo deploying' }
        }
    }
}
JF
echo "Jenkinsfile written"
