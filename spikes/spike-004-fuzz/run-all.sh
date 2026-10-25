#!/bin/sh
# DISPOSABLE SPIKE — NOT PRODUCTION CODE. Full property campaign; writes results/*.json
set -u
BIN=${BIN:-/tmp/spike004-fuzz}
OUT=results${TAG:+-$TAG}
mkdir -p $OUT/regressions
$BIN -selftest > $OUT/selftest.txt 2>&1
$BIN -runs ${RUNS:-12000} -seed 1      -intent=true  -out $OUT/intent.json  -regress $OUT/regressions > /dev/null 2>&1; echo "intent exit=$?"
$BIN -runs ${NAIVE_RUNS:-3000} -seed 500000 -intent=false -out $OUT/naive.json   -regress $OUT/regressions > /dev/null 2>&1; echo "naive exit=$?"
for m in nocycle nocascade unstablesort dropack silentdrop noauthz; do
  $BIN -runs ${MUT_RUNS:-600} -seed 900000 -intent=true -mut $m -out $OUT/mut-$m.json -regress $OUT/regressions > /dev/null 2>&1; echo "mut $m exit=$?"
done
