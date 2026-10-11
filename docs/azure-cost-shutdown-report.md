# Azure cost and shutdown report

Verified on 11 October 2026, approximately 03:39 UTC.

## Summary

- Azure AI Search is deleted. It was not restarted or recreated.
- The Function App is stopped, both background jobs are disabled, and all
  remaining alert rules are disabled.
- Storage is retained and **still incurs charges**. This is not a zero-cost
  shutdown.
- The attempted subscription migration failed. Project resources remain in
  `blogme (old)`; no resources were found in the target credit subscription.
- Azure reports **GBP 136.52 in incurred costs** across the available monthly
  records. The unpaid balance could not be verified because invoice access was
  denied.

## Historical costs

Source: Azure Cost Management `ActualCost`, grouped by billing month and service
for subscription `blogme (old)`.

| Period | AI Search | Storage | Functions | Azure Monitor | Other | Total (GBP) |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| August 2026 | 27.51 | 11.80 | 3.20 | 0.53 | <0.01 | **43.04** |
| September 2026 | 53.54 | 12.86 | 3.55 | 1.78 | 0.00 | **71.72** |
| October 2026, month-to-date through 11 October | 17.99 | 3.23 | 0.00 | 0.53 | 0.00 | **21.75** |
| **All returned records** | **99.04** | **27.88** | **6.75** | **2.84** | **<0.01** | **136.52** |

Previous completed months total **GBP 114.77**, calculated before rounding.
Displayed cells may not sum exactly because each is rounded independently.
August's "Other" charge is bandwidth (approximately GBP 0.00015).

Queries covering January-December 2025 and January-October 2026 returned no
monthly rows before August 2026. This means no earlier costs were returned for
those queried periods, not that all historical invoices were checked.

### Incurred cost is not an unpaid invoice

The figures above are reported resource costs, **not a verified amount due**.
The subscription invoice query returned `Forbidden`: the signed-in account
was not authorized to view invoices. Payment status, outstanding balances,
invoice tax, and final invoice totals therefore remain unverified.

Cost reporting can lag usage. October's total may increase as usage before
shutdown is processed, and retained storage continues to accrue costs.
Deleting a resource does not cancel charges already incurred.

This report covers the project's old subscription, not all charges in the
account's unrelated `Projects` subscription.

## Final resource verification

| Resource | Verified state | Cost implications |
| --- | --- | --- |
| `srch-blogme-basic-b3d38b` | Deleted; lookup returned `ResourceNotFound`, absent from subscription resource inventory | No further provisioned Search service charges after deletion; prior usage may appear later |
| `func-blogme-b3d38b` | `Stopped`; always-ready configuration is an empty list | App executions stopped; no always-ready instances configured |
| Background jobs `discover` and `score` | Both `AzureWebJobs.*.Disabled` settings are `true` | Background processing remains disabled |
| `ASP-rgblogme-027d` | `FC1`, `FlexConsumption`, capacity 0 | Not a fixed-price dedicated App Service plan |
| Five scheduled-query alert rules | All `enabled: false` | Log-query alert evaluation disabled |
| `blogme-instances-scaling-out` metric alert | `enabled: false` | Remaining metric alert disabled |
| `blogme-index-storage-high` metric alert | Deleted with Search cleanup | No dangling Search storage alert |
| `stblogmeb3d38b` | Retained, `Standard_LRS`, Hot tier | **Storage capacity and any external transactions remain billable** |
| Application Insights and backing Log Analytics workspace | Retained; reported costs were GBP 0.00 in returned records | Not a guarantee of future zero cost; external ingestion or billable retention can still incur charges |
| Action groups | Retained | Notification usage can be billable; project alert rules are disabled |

The five disabled log-query rules are:

- `blogme-discovery-cursor-stalled`
- `blogme-discovery-not-running`
- `blogme-job-run-failed`
- `blogme-job-slow`
- `blogme-search-failing`

Stopping the Function App is separate from deleting Search. Neither resource
was restarted. Blogme's search, suggestions, and API health checks depend on
Search, so the application is intentionally unavailable, not functioning as
before.

### Data preservation

The Search index schema was saved in the local Copilot session artifacts as
`articles-index-schema-backup.json`. That backup is **not committed to this
repository** and is not an independent durable backup.

The approximately 2.14 million indexed documents were **not exported** before
Search deletion and are not preserved by the schema backup. Future restoration
requires recreating Search and re-indexing from available source data.

The storage account was not deleted: deleting it would permanently remove
remaining deployment files and saved project data/state. No decision to delete
that remaining data was confirmed.

## Subscription migration

| Subscription | Role | Current result |
| --- | --- | --- |
| `blogme (old)` | Original Pay-As-You-Go subscription | Still holds project resources |
| `blogme` | Renamed Visual Studio Enterprise credit subscription | Target resource group created, but no resources moved |

The credit subscription has spending limits enabled. Its exact remaining credit
balance was not available through the inspected APIs.

Actual move attempts failed with `ResourceMoveProviderValidationFailed` and
`NotFound` targeting `Microsoft.Compute/virtualMachines`, even after provider
registration and quota checks. A subset excluding the Function App and plan
also failed with that error. **The root cause is unconfirmed**; successful
validation calls did not establish that the real move would succeed.

No migration retry, Search recreation, or re-indexing is planned as part of this
shutdown.

## Recommendations

1. **Keep Search deleted and the app stopped until restoration is intentional.**
   Avoid provisioning or deployment scripts that recreate resources or restart
   the app. Preserve the disabled background-job settings.
2. **Verify the actual unpaid balance in Azure Portal.** Open Cost Management +
   Billing, select the appropriate billing account/profile, and review invoices
   and payment status. Obtain billing permissions if necessary. Do not treat
   GBP 136.52 as a confirmed outstanding debt.
3. **Decide separately what to do with retained storage.** September's
   GBP 12.86 and October's partial GBP 3.23 show it is not free. The earlier
   estimate of roughly GBP 3.65/month for everything remaining was incorrect:
   it extrapolated a partial month. Inspect capacity, transaction meters,
   versions, snapshots, and retention before estimating the new steady-state
   cost or changing access tiers. Export anything needed before deletion.
4. **Recheck costs after reporting catches up.** Review the next few days and
   the final October invoice to distinguish delayed pre-shutdown usage from
   new storage or monitoring charges. The GBP 50 budget is an alert threshold,
   not a hard spending cap.
5. **Defer migration until needed.** If resuming, investigate the exact move
   failure with Azure support rather than assuming a quota or Flex Consumption
   issue. After any successful move, update deployment subscription settings,
   RBAC, resource references, and alert scopes before deploying again.
6. **Move the schema backup to durable storage before ending the environment.**
   Restore Search only with an explicit budget and re-indexing plan.

## Actions performed for this shutdown

- Deleted Azure AI Search and its storage metric alert.
- Stopped the Function App and verified `Stopped`.
- Verified both background jobs are disabled.
- Disabled the last active scheduled-query rule and verified all five disabled.
- Disabled the remaining metric alert and verified it disabled.
- Audited resources in the old and target subscriptions.
- Preserved storage and monitoring resources; no stored-data deletion performed.
