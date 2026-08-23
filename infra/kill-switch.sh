#!/usr/bin/env bash
# Turns blogme off, and back on again.
#
# Two levers, because "something is wrong" has two shapes here. `stop` takes the whole
# app down and is the answer to a flood: App Service refuses at the front door, so a
# request never reaches the code and never bills an execution. `discovery off` stops
# only the crawler and is the answer to a bill climbing on its own — the site carries
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

# The timer's name as main.go registers it. Disabling a single function is an app
# setting rather than a deployment, so it needs no redeploy and no code change.
DISCOVERY_FUNCTION="${DISCOVERY_FUNCTION:-discover}"
DISCOVERY_SETTING="AzureWebJobs.${DISCOVERY_FUNCTION}.Disabled"

log() { printf '\n\033[36m==> %s\033[0m\n' "$1"; }
die() { printf '\033[31merror: %s\033[0m\n' "$1" >&2; exit 1; }
note() { printf '\033[33m  %s\033[0m\n' "$1"; }
field() { printf '  %-15s %s\n' "$1" "$2"; }

usage() {
	cat <<'EOF'
Usage: infra/kill-switch.sh <command>

  status           What is running, and what it is costing
  stop             Stop the app: search, health and discovery all refuse
  start            Start it again
  discovery off    Stop the crawler, leave the site serving
  discovery on     Start the crawler again

Environment: RESOURCE_GROUP, FUNCTION_APP, BUDGET, DISCOVERY_FUNCTION
EOF
}

# ask runs a read-only az query and hands back one clean value.
#
# The Azure CLI emits CRLF on Windows, and a trailing carriage return is silent
# trouble: it makes a comparison false against a value that prints identically, and a
# number unparseable. Stripped once here rather than at four call sites. A query that
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

# discovery_disabled reads the setting rather than the function list, because the
# setting is the thing this script controls and so the thing it can speak for. Unset
# is the default and the default is enabled.
discovery_disabled() {
	local value
	value="$(ask az functionapp config appsettings list \
		--name "$FUNCTION_APP" --resource-group "$RESOURCE_GROUP" \
		--query "[?name=='${DISCOVERY_SETTING}'].value | [0]")"
	[[ "${value,,}" == "true" ]]
}

cmd_status() {
	local current ceiling spend unit limit

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
		if discovery_disabled; then
			field "Discovery" "off (${DISCOVERY_SETTING}=true)"
		else
			field "Discovery" "running"
		fi
		;;
	unknown)
		# Three states, not two. Reporting a failed lookup as "refusing" would answer
		# "is it off?" with a confident yes on no evidence, which in the one situation
		# this script exists for is worse than admitting the question went unanswered.
		field "Search" "unknown"
		field "Discovery" "unknown"
		;;
	*)
		field "Search" "refusing"
		field "Discovery" "stopped with the app"
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
	echo "  stopped: search, health and discovery all refuse"
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
	note "The first request pays a cold start. Discovery resumes at its next scheduled"
	note "hour, continuing from the cursor rather than restarting the source list."
}

cmd_discovery() {
	case "${1:-}" in
	off)
		log "Disabling discovery"
		az functionapp config appsettings set \
			--name "$FUNCTION_APP" --resource-group "$RESOURCE_GROUP" \
			--settings "${DISCOVERY_SETTING}=true" --output none
		echo "  ${DISCOVERY_SETTING}=true"
		printf '\n'
		note "The site keeps serving; only the crawl stops, and with it the writes that"
		note "are most of what storage costs. Changing a setting restarts the host, so"
		note "the next search pays a cold start."
		printf '\n'
		echo "  Undo with: infra/kill-switch.sh discovery on"
		;;
	on)
		log "Enabling discovery"
		az functionapp config appsettings set \
			--name "$FUNCTION_APP" --resource-group "$RESOURCE_GROUP" \
			--settings "${DISCOVERY_SETTING}=false" --output none
		echo "  ${DISCOVERY_SETTING}=false"
		printf '\n'
		note "Resumes at the next scheduled hour, continuing from the cursor."
		;;
	*)
		die "expected 'discovery off' or 'discovery on'"
		;;
	esac
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
discovery)
	shift
	cmd_discovery "${1:-}"
	;;
*)
	usage
	die "unknown command: $1"
	;;
esac
