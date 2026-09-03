#!/bin/bash
# lab.jenkins.security-integration t1: add a 'Security Scan' stage that
# runs ~/repo/scan.sh, placed BETWEEN Test and Deploy. Bootstraps ~/repo
# and scan.sh (fx.jenkinsfile-basic.v1 has no handler).
set -uo pipefail
mkdir -p ~/repo && cd ~/repo
git rev-parse --is-inside-work-tree >/dev/null 2>&1 || git init -q
[ -f scan.sh ] || { printf '#!/bin/bash\necho "scanning"; exit 0\n' > scan.sh; chmod +x scan.sh; }
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
        stage('Security Scan') {
            steps { sh './scan.sh' }
        }
        stage('Deploy') {
            steps { sh 'echo deploying' }
        }
    }
}
JF
echo "security-integration Jenkinsfile written"
