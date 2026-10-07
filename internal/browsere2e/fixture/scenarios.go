package main

const slowBrowserScenario = `#!/bin/sh
set -eu

run_id=${1:-}
case "$run_id" in
	''|*[!A-Za-z0-9]*)
		echo "run ID must contain only ASCII letters and digits" >&2
		exit 2
		;;
esac

trigger_file=".terminal-e2e-${run_id}-trigger"
progress_file=".terminal-e2e-${run_id}-progress"
done_file=".terminal-e2e-${run_id}-done"
continuous_file=".terminal-e2e-${run_id}-continuous"
stop_file=".terminal-e2e-${run_id}-stop"
mode=${2:-finite}

printf 'READY_%s\n' "$run_id"
while [ ! -f "$trigger_file" ]; do
	sleep 0.02
done

last_phase=4
if [ "$mode" = "continuous" ]; then
	last_phase=10
fi

phase=1
while [ "$phase" -le "$last_phase" ]; do
	block=0
	while [ "$block" -lt 80 ]; do
		printf '%1024s' ' '
		block=$((block + 1))
	done
	printf '\033[2J\033[HINTERMEDIATE_%s_%s\n' "$run_id" "$phase"
	if [ "$phase" -eq 1 ]; then
		: > "$progress_file"
	fi
	sleep 0.05
	phase=$((phase + 1))
done

if [ "$mode" = "continuous" ]; then
	: > "$continuous_file"
	tick=1
	while [ ! -f "$stop_file" ]; do
		printf '\rLIVE_%s_%s' "$run_id" "$tick"
		tick=$((tick + 1))
		sleep 0.01
	done
fi

printf '\033[2J\033[HFINAL_%s\n' "$run_id"
: > "$done_file"
IFS= read -r line
printf 'INPUT_%s:%s\n' "$run_id" "$line"
`

const trackingFailureScenario = `#!/bin/sh
set -eu

run_id=${1:-}
case "$run_id" in
	''|*[!A-Za-z0-9]*)
		echo "run ID must contain only ASCII letters and digits" >&2
		exit 2
		;;
esac

trigger_file=".terminal-tracking-${run_id}-trigger"
progress_file=".terminal-tracking-${run_id}-progress"

printf '\033[2J\033[HREADY_%s\n' "$run_id"
while [ ! -f "$trigger_file" ]; do
	sleep 0.02
done
printf 'PROGRESS_%s\n' "$run_id"
: > "$progress_file"
IFS= read -r line
printf 'ACCEPTED_%s:%s\n' "$run_id" "$line"
`

const trackingQualityScenario = `#!/bin/sh
set -eu

run_id=${1:-}
case "$run_id" in
	''|*[!A-Za-z0-9]*)
		echo "run ID must contain only ASCII letters and digits" >&2
		exit 2
		;;
esac

trigger_file=".terminal-quality-${run_id}-trigger"
progress_file=".terminal-quality-${run_id}-progress"

printf '\033[2J\033[HQUALITY_READY_%s\n' "$run_id"
while [ ! -f "$trigger_file" ]; do
	sleep 0.02
done
block=0
while [ "$block" -lt 2304 ]; do
	printf '%1024s' ' '
	block=$((block + 1))
	if [ $((block % 64)) -eq 0 ]; then
		sleep 0.01
	fi
done
printf '\033[2J\033[HDEGRADED_SCREEN_%s\n' "$run_id"
: > "$progress_file"
IFS= read -r line
printf 'DEGRADED_INPUT_%s:%s\n' "$run_id" "$line"
IFS= read -r line
if [ "$line" = "RESET_${run_id}" ]; then
	printf '\033c\033[2J\033[HTRUSTED_SCREEN_%s\n' "$run_id"
else
	printf 'UNEXPECTED_RESET_%s:%s\n' "$run_id" "$line"
fi
sleep 30
`

const resizeDeliveryScenario = `#!/bin/sh
set -eu

run_id=${1:-}
case "$run_id" in
	''|*[!A-Za-z0-9]*)
		echo "run ID must contain only ASCII letters and digits" >&2
		exit 2
		;;
esac

report_resize() {
	set -- $(stty size)
	printf 'RESIZE_APPLIED_%s:%sx%s\n' "$run_id" "$2" "$1"
}

trap report_resize WINCH
printf 'RESIZE_READY_%s\n' "$run_id"
while :; do
	sleep 0.05
done
`

const navigationScenario = `#!/bin/sh
set -eu

run_id=${1:-}
case "$run_id" in
	''|*[!A-Za-z0-9]*)
		echo "run ID must contain only ASCII letters and digits" >&2
		exit 2
		;;
esac

trigger_file=".terminal-navigation-${run_id}-trigger"
progress_file=".terminal-navigation-${run_id}-progress"
done_file=".terminal-navigation-${run_id}-done"
printf '\033[2J\033[HNAV_READY_%s\n' "$run_id"
marker=1
(
	while [ ! -f "$trigger_file" ]; do
		sleep 0.02
	done
	while [ "$marker" -le 40 ]; do
		printf 'NAV_%s_%02d\n' "$run_id" "$marker"
		printf '%s\n' "$marker" > "$progress_file"
		sleep 0.05
		marker=$((marker + 1))
	done
	printf '\033[2J\033[HNAV_FINAL_%s\n' "$run_id"
	: > "$done_file"
) &
while IFS= read -r line; do
	case "$line" in
		dimensions_*)
			marker_id=${line#dimensions_}
			set -- $(stty size)
			printf 'NAV_DIMS_%s:%sx%s\n' "$marker_id" "$2" "$1"
			;;
		*)
			printf 'NAV_INPUT_%s:%s\n' "$run_id" "$line"
			;;
	esac
done
`

const renderStallScenario = `#!/bin/sh
set -eu

run_id=${1:-}
case "$run_id" in
	''|*[!A-Za-z0-9]*)
		echo "run ID must contain only ASCII letters and digits" >&2
		exit 2
		;;
esac

first_file=".terminal-render-stall-${run_id}-first"
progress_file=".terminal-render-stall-${run_id}-progress"
final_file=".terminal-render-stall-${run_id}-final"
done_file=".terminal-render-stall-${run_id}-done"
printf '\033[2J\033[HSTALL_READY_%s\n' "$run_id"
while [ ! -f "$first_file" ]; do
	sleep 0.02
done
printf 'STALL_FIRST_%s\n' "$run_id"
printf 'first\n' > "$progress_file"
while [ ! -f "$final_file" ]; do
	sleep 0.02
done
marker=1
while [ "$marker" -le 40 ]; do
	printf 'STALL_PROGRESS_%s_%02d_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx\n' "$run_id" "$marker"
	marker=$((marker + 1))
done
printf 'STALL_FINAL_%s\n' "$run_id"
printf 'final\n' > "$progress_file"
: > "$done_file"
while IFS= read -r line; do
	case "$line" in
		dimensions_*)
			marker_id=${line#dimensions_}
			set -- $(stty size)
			printf 'STALL_DIMS_%s:%sx%s\n' "$marker_id" "$2" "$1"
			;;
		*)
			printf 'STALL_INPUT_%s:%s\n' "$run_id" "$line"
			;;
	esac
done
`

const synchronizedOutputRaceScenario = `#!/bin/sh
set -eu

run_id=${1:-}
case "$run_id" in
	''|*[!A-Za-z0-9]*)
		echo "run ID must contain only ASCII letters and digits" >&2
		exit 2
		;;
esac

trigger_file=".terminal-synchronized-output-${run_id}-trigger"
printf '\033[2J\033[HBASELINE_FRAME_%s\n' "$run_id"
while [ ! -f "$trigger_file" ]; do
	sleep 0.01
done

trap 'printf "\033[?2026l\033[?25h"' EXIT
printf '\033[?25l\033[?2026h\033[2J\033[HMODEL_FRAME_%s\nSTALE_DOM_REPAINT_BUG\033[?2026l\033[?2026h' "$run_id"
sleep 5
`

const unicodeWidthScenario = `#!/bin/sh
set -eu

run_id=${1:-}
case "$run_id" in
	''|*[!A-Za-z0-9]*)
		echo "run ID must contain only ASCII letters and digits" >&2
		exit 2
		;;
esac

printf '\033[2J\033[H\360\237\220\271 x\r\033[3C\033[K'
printf '\033[2;1HWIDTH_REDRAW_READY_%s\n' "$run_id"
# Cross the fixture's checkpoint threshold without changing the visible screen.
printf '\033]0;%2048s\007' ''
while :; do
	sleep 1
done
`

const semanticRestoreScenario = `#!/bin/sh
set -eu

run_id=${1:-}
case "$run_id" in
	''|*[!A-Za-z0-9]*)
		echo "run ID must contain only ASCII letters and digits" >&2
		exit 2
		;;
esac

ready_file=".terminal-semantic-${run_id}-ready"
printf '\033[?1049h\033[2J\033[H'
printf 'SEMANTIC_HEADER_%s' "$run_id"
printf '\033[3;5H\033[38;2;12;200;90mCOLOR_%s_é\033[0m' "$run_id"
printf '\033[4;8r'
printf '\033[10;1HSEMANTIC_FOOTER_%s' "$run_id"
printf '\033[6;7H\033[?1h'
: > "$ready_file"

original_stty=$(stty -g)
stty raw -echo min 1 time 0
key_hex=$(dd bs=1 count=3 2>/dev/null | od -An -tx1 | tr -d ' \n')
stty "$original_stty"
printf '\033[8;1HKEY_%s:%s\n' "$run_id" "$key_hex"
printf '\033[10;1HSEMANTIC_FOOTER_%s' "$run_id"
printf '\033[?1l\033[r\033[11;1H'
IFS= read -r line
printf 'SEMANTIC_INPUT_%s:%s\n' "$run_id" "$line"
sleep 30
`

const ownershipScenario = `#!/bin/sh
set -eu

run_id=${1:-}
case "$run_id" in
	''|*[!A-Za-z0-9]*)
		echo "run ID must contain only ASCII letters and digits" >&2
		exit 2
		;;
esac

printf '\033[2J\033[HOWNER_READY_%s\n' "$run_id"
while IFS= read -r line; do
	printf 'OWNER_INPUT_%s:%s\n' "$run_id" "$line"
done
`
