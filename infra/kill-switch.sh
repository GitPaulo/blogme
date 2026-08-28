#!/usr/bin/env bash
# Turns blogme off, and back on again.
#
# Two levers, because "something is wrong" has two shapes here. `stop` takes the whole
# app down and is the answer to a flood: App Service refuses at the front door, so a
# request never reaches the code and never bills an execution. `jobs off` stops the
# background timers and is the answer to a bill climbing on its own — the site carries
# on serving and readers see nothing.
#
# Neither lever touches the floor. Azure AI Search Basic bills by the hour whether or
# not a query ever arrives, and the only way to stop it is to delete the service, which
# takes the index with it. `status` says so every time, so that "everything is off" is
# never something this script lets you assume.
#
# No confirmation prompt: the subcommands are explicit enough that none is reached by
# accident, both are undone by one command, and a tool for emergencies should not be
# waiting on an answer. Each one prints its own undo line instead.

set -euo pipefail

RESOURCE_GROUP="${RESOURCE_GROUP:-rg-blogme}"
FUNCTION_APP="${FUNCTION_APP:-func-blogme-b3d38b}"
BUDGET="${BUDGET:-blogme-alert-budget}"

log() { printf '\n\033[36m==> %s\033[0m\n' "$1"; }
die() { printf '\033[31merror: %s\033[0m\n' "$1" >&2; exit 1; }
note() { printf '\033[33m  %s\033[0m\n' "$1"; }
field() { printf '  %-15s %s\n' "$1" "$2"; }

usage() {
	cat <<'EOF'
Usage: infra/kill-switch.sh <command>

  status           What is running, and what it is costing
  stop             Stop the app: every function refuses
  start            Start it again
  jobs off [name]  Stop the background timers, leave the site serving
  jobs on  [name]  Start them again

  Both jobs commands take every timer by default, or one named timer.

Environment: RESOURCE_GROUP, FUNCTION_APP, BUDGET
EOF
}

# ask runs a read-only az query and hands back one clean value.
#
# The Azure CLI emits CRLF on Windows, and a trailing carriage return is silent
# trouble: it makes a comparison false against a value that prints identically, and a
# number unparseable. Stripped once here rather than at each call site. A query that
# fails yields the empty string, which every caller reads as "unknown" rather than as
# "off" — a lookup that did not work must never look like a service that is not
# running.
ask() {
	local value
	value="$("$@" -o tsv 2>/dev/null | tr -d '\r')" || true
	printf '%s' "$value"
}

# state reports what App Service says the site is doing: Running, Stopped, or unknown
# if the lookup itself failed.
state() {
	local value
	value="$(ask az resource show \
		--resource-group "$RESOURCE_GROUP" \
		--resource-type Microsoft.Web/sites \
		--name "$FUNCTION_APP" \
		--query properties.state)"
	printf '%s' "${value:-unknown}"
}

# timer_rows lists the app's timer functions as "name<TAB>disabled".
#
# Asked of the app rather than written down here, because a list written down is a list
# that goes stale. This script was first written when discovery was the only timer, and
# named it directly; a second timer was added later and the lever meant to stop
# background work quietly left it running. A timer is recognised by its trigger, so
# whatever the app registers is what this covers, including whatever it registers next.
timer_rows() {
	ask az functionapp function list \
		--name "$FUNCTION_APP" --resource-group "$RESOURCE_GROUP" \
		--query "[?config.bindings[?type=='timerTrigger']].[name, isDisabled]" \
		| sed 's|^[^/]*/||'
}

timer_names() { timer_rows | cut -f1; }

cmd_status() {
	local current ceiling spend unit limit name disabled

	current="$(state)"
	ceiling="$(ask az functionapp scale config show \
		--name "$FUNCTION_APP" --resource-group "$RESOURCE_GROUP" \
		--query maximumInstanceCount)"

	log "blogme"
	field "Function app" "$FUNCTION_APP"
	field "State" "$current"

	# A stopped app answers nothing, whatever the per-function settings say, so those
	# settings are not worth reporting as though they were still in charge.
	case "$current" in
	Running)
		field "Search" "serving"
		while IFS=$'\t' read -r name disabled; do
			[[ -n "$name" ]] || continue
			if [[ "${disabled,,}" == "true" ]]; then
				field "Job $name" "off"
			else
				field "Job $name" "running"
			fi
		done <<<"$(timer_rows)"
		;;
	unknown)
		# Three states, not two. Reporting a failed lookup as "refusing" would answer
		# "is it off?" with a confident yes on no evidence, which in the one situation
		# this script exists for is worse than admitting the question went unanswered.
		field "Search" "unknown"
		field "Jobs" "unknown"
		;;
	*)
		field "Search" "refusing"
		field "Jobs" "stopped with the app"
		;;
	esac

	field "Scale ceiling" "${ceiling:-unknown} instances"

	# Read from the budget rather than from the cost API: it is one call, it is the
	# figure the alert emails quote, and "of what" is the half that makes a number mean
	# something. Absent or unreadable, the line is simply left out.
	read -r spend unit limit <<<"$(ask az consumption budget show \
		--budget-name "$BUDGET" \
		--query "[currentSpend.amount, currentSpend.unit, amount]" | paste -sd' ' -)" || true
	if [[ -n "${spend:-}" && -n "${limit:-}" ]]; then
		field "Spend" "$(printf '%s %.2f of %s %.2f this month' \
			"$unit" "$spend" "$unit" "$limit")"
	fi

	printf '\n'
	note "Azure AI Search Basic bills ~GBP 55/month whether or not anything is running."
	note "No command here stops that; only deleting the service does, index and all."
}

cmd_stop() {
	log "Stopping $FUNCTION_APP"
	az functionapp stop --name "$FUNCTION_APP" --resource-group "$RESOURCE_GROUP" --output none
	echo "  stopped: every function refuses, timers included"
	printf '\n'
	note "App Service now refuses at the front door, so a flood costs no executions."
	note "The deploy workflow gates on /api/health, so deploys fail until this is undone."
	printf '\n'
	echo "  Undo with: infra/kill-switch.sh start"
}

cmd_start() {
	log "Starting $FUNCTION_APP"
	az functionapp start --name "$FUNCTION_APP" --resource-group "$RESOURCE_GROUP" --output none
	echo "  started"
	printf '\n'
	note "The first request pays a cold start. Timers resume at their next scheduled"
	note "hour, continuing from where they left off rather than starting over."
}

cmd_jobs() {
	local action="${1:-}" only="${2:-}" disabled names name
	local -a settings=()

	case "$action" in
	off) disabled=true ;;
	on) disabled=false ;;
	*) die "expected 'jobs off' or 'jobs on', either optionally naming one timer" ;;
	esac

	names="$(timer_names)"
	[[ -n "$names" ]] || die "found no timers on $FUNCTION_APP; is it deployed?"

	if [[ -n "$only" ]]; then
		grep -qxF "$only" <<<"$names" \
			|| die "no timer named '$only' (found: $(tr '\n' ' ' <<<"$names"))"
		names="$only"
	fi

	while read -r name; do
		[[ -n "$name" ]] && settings+=("AzureWebJobs.${name}.Disabled=${disabled}")
	done <<<"$names"

	log "Turning background jobs $action"
	# One call however many timers are named, because every settings change restarts
	# the host and there is no reason to pay for that twice.
	az functionapp config appsettings set \
		--name "$FUNCTION_APP" --resource-group "$RESOURCE_GROUP" \
		--settings "${settings[@]}" --output none
	printf '  %s\n' "${settings[@]}"
	printf '\n'

	if [[ "$action" == off ]]; then
		note "The site keeps serving; only the background work stops, and with it the"
		note "writes and third-party calls that are most of what it costs. Changing a"
		note "setting restarts the host, so the next search pays a cold start."
		printf '\n'
		echo "  Undo with: infra/kill-switch.sh jobs on"
	else
		note "Each resumes at its next scheduled hour, continuing from where it left off."
	fi
}

case "${1:-}" in
-h | --help | "")
	usage
	exit 0
	;;
esac

# Checked once, here, so that every command fails the same clear way rather than each
# failing differently somewhere in the middle of doing its job.
az account show --query id -o tsv >/dev/null 2>&1 \
	|| die "not signed in to Azure (run 'az login')"

case "$1" in
status) cmd_status ;;
stop) cmd_stop ;;
start) cmd_start ;;
jobs)
	shift
	cmd_jobs "${1:-}" "${2:-}"
	;;
*)
	usage
	die "unknown command: $1"
	;;
esac
