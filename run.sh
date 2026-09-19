#!/usr/bin/env bash
# shhgit ultra-speed scanner (web dashboard + in-process scanner)
# Restart with: ~/shhgit2/run.sh
cd "$(dirname "$0")"
exec ./shhgit --web --web-host 0.0.0.0 --web-port 8080 -threads 8 --config-path "$(pwd)" >> run.log 2>&1
