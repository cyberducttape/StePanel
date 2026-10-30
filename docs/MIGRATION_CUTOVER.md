# cPanel migration cutover checklist

The cpmove import restores selected website files and databases and stages
mailbox data under StePanel's private mail root. It does not change public DNS,
activate mail delivery, import source credentials or aliases, or recreate cron
jobs. Review the import job's migration evidence and complete the relevant
steps below before moving customer traffic.

## Before import

1. Record the source account, domains, document roots, database names, PHP
   version/extensions, mailbox names, aliases, forwarders, mail filters,
   scheduled tasks, and current TLS certificates.
2. Export the authoritative DNS zone. Record A/AAAA, CNAME, MX, TXT (including
   SPF, DKIM, and DMARC), SRV, and current TTL values. DNS is managed at the
   authoritative provider; a cpmove restore does not copy or update it.
3. Confirm destination disk capacity and database compatibility. Inspect the
   archive and review the source-to-destination migration analysis before
   starting a restore. Export the cpmove preflight evidence as JSON or Markdown
   so domain mappings, database-version evidence, mailbox layout, and unknowns
   are available during change review.
4. Lower relevant DNS TTLs at least one existing TTL period before the planned
   switch. Keep the old host and its backups available through the rollback
   window.

## Verify the destination

1. Review the durable import result by category: account mapping, website roots,
   database dumps, mailbox data, domain placement, and every skipped or
   uninspected component. Download the post-migration JSON or Markdown report
   after the job completes. A bounded list reports how many additional names
   were omitted from the job summary. Treat `passed`, `warning`, `blocked`, and
   `unknown` distinctly; staged mail or a parsed archive is not a live service
   verification.
2. Verify database imports and credentials, then restore to staging and test
   the application against the destination database. Check representative
   read/write flows, uploads, plugins, and scheduled application tasks.
3. Mailbox data is staged, not live. Configure destination mailbox identities
   and credentials, aliases, forwarders, filters, quotas, and TLS. Test inbound
   and outbound delivery while the old MX remains authoritative where possible.
   Prepare SPF, DKIM, and DMARC changes with the mail operator.
4. Review each source cron entry. Recreate required jobs using the managed task
   workflow, update paths and environment assumptions, and run each task once
   manually before enabling its schedule.
5. Obtain and verify destination TLS certificates after the domain validation
   prerequisites are met. Test HTTPS and application behavior on the destination
   before changing public DNS.

## Cut over and recover

1. Freeze or account for writes on the source, take a final backup, and repeat
   the application/database verification for changes made since the initial
   import.
2. Change A/AAAA/CNAME and MX records at the authoritative DNS provider only
   after website and mail checks pass. Preserve the previous records and note
   when caches are expected to expire.
3. Monitor web requests, mail queues, database errors, and scheduled task
   results during propagation. Keep the old host serving or safely quiesced
   until the rollback window closes.
4. If verification fails, restore the saved DNS values and resume the source
   according to the operator's write-freeze plan. Do not delete the source
   account or its backups until application, database, and mail acceptance is
   recorded.

DNS propagation, mailbox delivery, and source cron equivalence are not claimed
by archive integrity or by a successful cpmove import job. Record those as
separate cutover evidence.
