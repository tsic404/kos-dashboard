#!/bin/sh
# qa/acceptance.sh runs the four window-layout acceptance cases of this issue
# and prints one line per case and side. QA only executes it: it takes no
# arguments, changes nothing, and its exit status is the whole verdict.
#
# Each case is one directory under tests/acceptance/ (design case DASH-AC-70):
#   DASH-TC-148  cross-browser layout, viewport clamp, removed display
#   DASH-TC-149  record and serialized limits, migration, higher version
#   DASH-TC-150  interaction state stays out of objects and preferences, outage
#   DASH-TC-161  objects, control-plane database and preference values carry none
#
# A case has a Go half (tc*_test.go: the control plane and the front) and a
# browser half (shell.test.js: the shell module). A half that is not present is
# reported as MISSING and fails the run: a deleted or never-written case must
# not report a clean verdict.
set -eu

cd "$(dirname "$0")/.."

GO=${GO:-go}
JS=${JS:-node}
CASES=${CASES:-"DASH-TC-148 DASH-TC-149 DASH-TC-150 DASH-TC-161"}

# The Go halves boot the pinned control plane; the Makefile owns the version,
# the download and the checksum.
make envtest-assets >/dev/null
KUBEBUILDER_ASSETS=$(pwd)/bin/envtest
export KUBEBUILDER_ASSETS

failures=0

printf '%s\n' "== 验收用例（KUBEBUILDER_ASSETS=$KUBEBUILDER_ASSETS）"
for case in $CASES; do
	directory="tests/acceptance/$case"
	if [ ! -d "$directory" ]; then
		printf '%s\n' "FAIL     $case  仓内没有这个用例目录"
		failures=$((failures + 1))
		continue
	fi

	if ls "$directory"/*_test.go >/dev/null 2>&1; then
		if $GO test "./$directory/" -count=1; then
			printf '%s\n' "PASS     $case  Go 侧"
		else
			printf '%s\n' "FAIL     $case  Go 侧"
			failures=$((failures + 1))
		fi
	else
		printf '%s\n' "MISSING  $case  Go 侧"
		failures=$((failures + 1))
	fi

	browserHalf=0
	for file in "$directory"/*.test.js; do
		[ -e "$file" ] || continue
		browserHalf=$((browserHalf + 1))
		if $JS --test "$file"; then
			printf '%s\n' "PASS     $case  $(basename "$file")"
		else
			printf '%s\n' "FAIL     $case  $(basename "$file")"
			failures=$((failures + 1))
		fi
	done
	if [ "$browserHalf" -eq 0 ]; then
		printf '%s\n' "MISSING  $case  浏览器侧"
		failures=$((failures + 1))
	fi
done

printf '\n%d 项未通过\n' "$failures"
[ "$failures" -eq 0 ]
