# StePanel compared with other hosting panels

This page is meant to help you decide honestly, including when StePanel is
not the right choice yet. Statements about other products describe their
general model and focus; check each vendor's current documentation before
deciding, because features and pricing change.

## At a glance

| | StePanel | cPanel & WHM | Plesk | HestiaCP | CloudPanel |
| --- | --- | --- | --- | --- | --- |
| Licence | Open source (MIT) | Commercial, priced by account count | Commercial | Open source | Free to use |
| Maturity | Operator Beta | Long-established | Long-established | Established | Established |
| Typical use | Single server you operate | Shared hosting and resellers | Shared hosting, agencies, Windows hosting | Single server, web and mail | PHP and Node application servers |
| cPanel account import | Yes, inspected and staged before import | Native | Migration tools | Community tooling | Limited |
| Published recovery testing | Yes, every result linked ([proof](./PROOF.md)) | Not published | Not published | Not published | Not published |
| Tamper-evident audit trail | Yes, with optional off-host signed checkpoints | Logs | Logs | Logs | Logs |
| Reversible site suspension | Yes | Yes | Yes | Yes | Varies |
| Integrated email hosting | Optional install; cPanel mailboxes are staged, not yet activated automatically | Full | Full | Full | No |
| DNS server | Provider adapters, no built-in DNS server | Built in | Built in | Built in | No |
| Reseller accounts | No | Yes | Yes | Yes | No |
| Multi-server / clustering | No (not planned for 1.0) | Yes | Yes | No | No |

"Not published" means we did not find public, per-commit recovery test
results; it does not mean those products are untested.

## Choose StePanel if

- You run your own sites, or a small agency's client sites, on one server.
- You are leaving cPanel and want to review a migration plan before it runs.
- You value being able to see what has been proven about backups, restores,
  upgrades, and crash recovery.
- You prefer a small, readable codebase you can audit, under the MIT licence.

## Choose something else for now if

- You sell hosting to untrusted customers: StePanel does not yet treat sites
  on one server as a security boundary against hostile tenants.
- You need resellers, a built-in DNS server, or fully managed email today.
- You need multi-server clustering or high availability.
- You need a vendor support contract or a long production track record.

## Moving from cPanel

StePanel imports cPanel `cpmove` backups through a plan you review first:
it inspects the archive, rejects unsafe entries, checks the destination for
conflicts and capacity, and then imports into staging. See the
[cPanel migration guide](./CPMOVE_IMPORTS.md) and the
[migration cutover guide](./MIGRATION_CUTOVER.md). Test a migration on a
disposable server first; StePanel is in Operator Beta.
