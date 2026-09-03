#!/bin/bash
# lab.ansible.basics t1: write ~/playbook.yml (hosts: localhost, two
# tasks: create dir /tmp/ansible-demo, create file marker.txt in it).
# Validators: file exists; valid YAML; contains "hosts: localhost".
set -uo pipefail
cat > ~/playbook.yml <<'YML'
---
- hosts: localhost
  connection: local
  gather_facts: false
  tasks:
    - name: create demo directory
      file:
        path: /tmp/ansible-demo
        state: directory
        mode: '0755'
    - name: create marker file
      copy:
        dest: /tmp/ansible-demo/marker.txt
        content: "created by ansible\n"
YML
echo "playbook.yml written"
