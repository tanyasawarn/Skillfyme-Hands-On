#!/bin/bash
# lab.jenkins.advanced-pipelines t1: Jenkinsfile with retry(3) around the
# Test step and a top-level environment { BUILD_TARGET = 'production' }.
# Bootstraps ~/repo (fx.jenkinsfile-basic.v1 has no handler).
set -uo pipefail
mkdir -p ~/repo && cd ~/repo
git rev-parse --is-inside-work-tree >/dev/null 2>&1 || git init -q
cat > Jenkinsfile <<'JF'
pipeline {
    agent any
    environment { BUILD_TARGET = 'production' }
    stages {
        stage('Build') {
            steps { sh 'echo building for $BUILD_TARGET' }
        }
        stage('Test') {
            steps {
                retry(3) {
                    sh 'echo running tests'
                }
            }
        }
        stage('Deploy') {
            steps { sh 'echo deploying to $BUILD_TARGET' }
        }
    }
}
JF
echo "advanced Jenkinsfile written"
