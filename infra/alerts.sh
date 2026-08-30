#!/usr/bin/env bash
# Creates the alert rules that watch blogme, and the action group they notify.
#
# These lived only in the portal until now. Nothing in this repo said what was being
# watched or at what threshold, and a re-provision would have dropped every one of them
# without saying so — alert rules being the part of an environment most easily lost and
# least missed until the day they were wanted.
#
# Safe to re-run, and worth re-running: every rule is written unconditionally rather
# than skipped when it already exists, because Azure's create is an upsert. Re-running
# is therefore how a rule edited in the portal is put back, not merely how a missing one
# is filled in.
#
# Not here: the cost budget. It is a consumption object rather than an alert rule, on a
# different CLI surface, and it notifies a different address than these do.

set -euo pipefail

# Git Bash rewrites anything shaped like a Unix path into a Windows one, which turns an
# ARM resource id into C:/Program Files/Git/subscriptions/... and every scope below is
# one. ARM then rejects it with an error that never mentions the cause. Harmless on any
# other platform.
export MSYS_NO_PATHCONV=1 MSYS2_ARG_CONV_EXCL='*'

RESOURCE_GROUP="${RESOURCE_GROUP:-rg-blogme}"
FUNCTION_APP="${FUNCTION_APP:-func-blogme-b3d38b}"
SEARCH_SERVICE="${SEARCH_SERVICE:-srch-blogme-basic-b3d38b}"
ACTION_GROUP="${ACTION_GROUP:-ag-blogme-ops}"
ALERT_EMAIL="${ALERT_EMAIL:-projects.paulo.santos98@gmail.com}"

# Two of three consecutive periods, so one transient failure stays quiet while a real
# one still reaches someone within the hour.
PERSISTENT="at least 2 violations out of 3 aggregated points"

log() { printf '\n\033[36m==> %s\033[0m\n' "$1"; }
die() { printf '\033[31merror: %s\033[0m\n' "$1" >&2; exit 1; }

# az emits CRLF on Windows, and a carriage return inside a resource id is rejected by
# ARM with an error that quotes the id back without showing what is wrong with it.
#
# A lookup that fails returns the empty string rather than failing the script: az exits
# 3 when a resource is absent, and with pipefail set that status would otherwise travel
# out through the assignment and end the run under set -e — before the check below it
# could say which name was wrong. Every caller tests what it got back.
tsv() {
	local value
	value="$(az "$@" -o tsv 2>/dev/null | tr -d '\r')" || true
	printf '%s' "$value"
}

az account show --query id -o tsv >/dev/null 2>&1 \
	|| die "not signed in to Azure (run 'az login')"

log "Resolving scopes"

SITE_ID="$(tsv functionapp show --name "$FUNCTION_APP" --resource-group "$RESOURCE_GROUP" --query id)"
[[ -n "$SITE_ID" ]] || die "function app '$FUNCTION_APP' not found in $RESOURCE_GROUP"

SEARCH_ID="$(tsv search service show --name "$SEARCH_SERVICE" --resource-group "$RESOURCE_GROUP" --query id)"
[[ -n "$SEARCH_ID" ]] || die "search service '$SEARCH_SERVICE' not found in $RESOURCE_GROUP"

# Asked of the Application Insights component rather than named here. A workspace-based
# component keeps its data in a Log Analytics workspace that Azure created in a resource
# group of its own choosing, whose name is derivable from nothing in this repo — and the
# log-query rules below have to be scoped to that workspace, not to the component.
WORKSPACE_ID="$(tsv monitor app-insights component show \
	--app "$FUNCTION_APP" --resource-group "$RESOURCE_GROUP" --query workspaceResourceId)"
[[ -n "$WORKSPACE_ID" ]] || die "no workspace behind Application Insights component '$FUNCTION_APP'"

printf '  site       %s\n' "${SITE_ID##*/}"
printf '  search     %s\n' "${SEARCH_ID##*/}"
printf '  workspace  %s\n' "${WORKSPACE_ID##*/}"

log "Action group"
az monitor action-group create \
	--name "$ACTION_GROUP" \
	--resource-group "$RESOURCE_GROUP" \
	--short-name blogmeops \
	--action email ops "$ALERT_EMAIL" \
	--output none
echo "  ok: $ACTION_GROUP -> $ALERT_EMAIL"

AG_ID="$(tsv monitor action-group show --name "$ACTION_GROUP" --resource-group "$RESOURCE_GROUP" --query id)"
[[ -n "$AG_ID" ]] || die "action group '$ACTION_GROUP' could not be resolved after creating it"

# query_alert writes one log-query rule. The arguments every rule shares live here so
# that the rules themselves differ only where they mean to.
query_alert() {
	local name="$1" severity="$2" frequency="$3" window="$4" periods="$5" query="$6" description="$7"

	# Built up rather than interpolated, because a rule that wants the default of one
	# period passes no clause, and the trailing space that would leave behind is enough
	# for the condition parser to complain about.
	local condition="count 'Q' > 0"
	[[ -n "$periods" ]] && condition="$condition $periods"

	az monitor scheduled-query create \
		--name "$name" \
		--resource-group "$RESOURCE_GROUP" \
		--scopes "$WORKSPACE_ID" \
		--action-groups "$AG_ID" \
		--severity "$severity" \
		--evaluation-frequency "$frequency" \
		--window-size "$window" \
		--auto-mitigate true \
		--description "$description" \
		--condition "$condition" \
		--condition-query Q="$query" \
		--output none
	echo "  ok: $name"
}

# metric_alert writes one platform-metric rule. These read counters Azure publishes
# about a resource, so they keep working when the app is too broken to log anything.
metric_alert() {
	local name="$1" scope="$2" severity="$3" frequency="$4" window="$5" condition="$6" description="$7"

	az monitor metrics alert create \
		--name "$name" \
		--resource-group "$RESOURCE_GROUP" \
		--scopes "$scope" \
		--action "$AG_ID" \
		--severity "$severity" \
		--evaluation-frequency "$frequency" \
		--window-size "$window" \
		--description "$description" \
		--condition "$condition" \
		--output none
	echo "  ok: $name"
}

log "Log-query alerts"

# A timer is recognised by the shape of its telemetry rather than by name. Every timer
# the app registers is covered, including the next one: naming them is what let a second
# timer run for days with nothing watching it.
query_alert blogme-job-run-failed 1 1h 1h "$PERSISTENT" \
	"// A timer invocation carries no URL, so this covers every timer the app registers.
AppRequests | where isempty(Url) and Success == false" \
	"A background job failed. Matches on a timer invocation carrying no URL, so it covers every timer the app registers rather than a list of names that goes stale. Two of three consecutive hours, so one transient failure stays quiet."

query_alert blogme-job-slow 2 1h 1h "$PERSISTENT" \
	"// A timer invocation carries no URL, so this covers every timer the app registers.
AppRequests | where isempty(Url) and DurationMs > 900000" \
	"A background job ran past fifteen minutes. Discovery's p95 is around five minutes and scoring's under two, so this is several times the worst normal case. Covers every timer, by the same rule as the failure alert."

query_alert blogme-search-failing 2 15m 15m "$PERSISTENT" \
	"AppRequests | where Name == 'search' and Success == false" \
	"Searches are failing. Counts actual failed requests rather than a percentage, because at this query volume a single throttled query is 100 percent of a minute. Requires two of three consecutive periods, so a brief self-healing burst stays quiet."

# Discovery gets two rules because it has two ways to fail, and asking one query about
# both is what made it cry wolf. A log query cannot tell a stalled job from a job whose
# log lines have not arrived yet, and `summarize` without a `by` clause answers even over
# no rows at all — so the single rule that used to live here read an empty window as a
# dead cursor and paged at severity 1. It did exactly that on 2026-08-30 at 15:33, while
# discovery was in fact advancing every hour: an Azure ingestion stall delayed that
# afternoon's telemetry by up to six hours and dropped some of it outright.
#
# Both rules keep one period rather than two. The windows are already long, and a second
# period would mean waiting most of a day to hear that discovery had stopped.

# Requiring two observed passes is what makes this one safe. A cursor that is genuinely
# frozen still reports four passes in four hours, all on the same cursor; telemetry that
# is merely late reports fewer passes, and this now reads that as silence rather than as
# evidence of a stall.
query_alert blogme-discovery-cursor-stalled 1 1h 4h "" \
	"AppTraces | where Message has 'discovery pass complete' | extend c = extract('next_cursor=([A-Za-z0-9._-]+)',1,Message) | summarize passes = count(), cursors = dcount(c) | where passes >= 2 and cursors < 2" \
	"Discovery ran at least twice in 4h without advancing its cursor. Requires two observed passes, so that late telemetry reads as silence rather than as a stall."

# The other half of what the old rule claimed to cover: discovery not running at all.
# Twelve hours rather than four because this is the one that has to outlast a late log
# pipeline. Discovery completes a pass every hour without exception, so any 12h window
# should hold twelve; the worst ingestion delay yet seen lost seven consecutive hours of
# these lines, and the fewest in any 12h window over three days was five. Against a
# threshold of none, that is margin enough to mean what it says.
query_alert blogme-discovery-not-running 1 1h 12h "" \
	"AppTraces | where Message has 'discovery pass complete' | summarize passes = count() | where passes == 0" \
	"Discovery has not completed a pass in 12 hours. It runs hourly, so this is a dead timer rather than a slow one."

log "Metric alerts"

metric_alert blogme-instances-scaling-out "$SITE_ID" 2 PT5M PT15M \
	"avg InstanceCount > 5" \
	"Function app has sustained more instances than normal operation has ever needed (observed peak 2, cap 10). Averaged over 15 minutes so a deploy or a cold-start burst does not trip it."

# 9 GiB, which is 60 percent of the Basic tier's 15 GiB.
metric_alert blogme-index-storage-high "$SEARCH_ID" 2 PT1H PT6H \
	"avg IndexStorageUsage > 9663676416" \
	"Search index has passed 60 percent of the Basic tier's 15 GiB quota, averaged over 6 hours so indexing spikes do not trip it. Indexing fails outright at 100 percent."

log "Done"
printf '  %-32s %s\n' "RULE" "SEVERITY"
for rule in $(tsv monitor scheduled-query list --resource-group "$RESOURCE_GROUP" --query "[].name"); do
	printf '  %-32s sev%s\n' "$rule" \
		"$(tsv monitor scheduled-query show --name "$rule" --resource-group "$RESOURCE_GROUP" --query severity)"
done
for rule in $(tsv monitor metrics alert list --resource-group "$RESOURCE_GROUP" --query "[].name"); do
	printf '  %-32s sev%s\n' "$rule" \
		"$(tsv monitor metrics alert show --name "$rule" --resource-group "$RESOURCE_GROUP" --query severity)"
done
