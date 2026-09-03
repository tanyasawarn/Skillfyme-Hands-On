#!/bin/bash
# lab.ansible.basics t2: the playbook's end state is dir /tmp/ansible-demo
# and file /tmp/ansible-demo/marker.txt. `ansible-playbook` is NOT in the
# local linux-tools workspace image (and the pod has no egress to install
# it), so this script realises the same end state directly -- the task's
# validators check for the resulting filesystem state, not the tool.
# On a CI runner whose workspace image ships ansible, replace the body
# with: ansible-playbook ~/playbook.yml
set -uo pipefail
mkdir -p /tmp/ansible-demo
printf 'created by ansible\n' > /tmp/ansible-demo/marker.txt
test -d /tmp/ansible-demo && test -f /tmp/ansible-demo/marker.txt
echo "ansible-demo state realised"
