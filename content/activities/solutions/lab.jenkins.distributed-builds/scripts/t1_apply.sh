#!/bin/bash
# lab.jenkins.distributed-builds t1: top-level `agent none`, Build stage
# gets its own `agent { label 'linux-builder' }`. Bootstraps ~/repo.
set -uo pipefail
mkdir -p ~/repo && cd ~/repo
git rev-parse --is-inside-work-tree >/dev/null 2>&1 || git init -q
cat > Jenkinsfile <<'JF'
pipeline {
    agent none
    stages {
        stage('Build') {
            agent { label 'linux-builder' }
            steps { sh 'echo building on linux-builder' }
        }
        stage('Test') {
            agent any
            steps { sh 'echo testing' }
        }
        stage('Deploy') {
            agent any
            steps { sh 'echo deploying' }
        }
    }
}
JF
echo "distributed-builds Jenkinsfile written"
