# systemctl show fixtures

Real `systemctl show` output in the exact property order and format that
`stepanel-appctl resource-status` requests. Parsers must be tested against
these rather than hand-written integers: systemd prints `*USec` properties as
human timespans (for example `CPUQuotaPerSecUSec=1.500000s`), and unlimited or
unset values as `infinity` or `[not set]`.

Capture a new fixture with:

    systemd-run --user --unit=probe -p CPUQuota=150% -p MemoryMax=256M sleep 30
    systemctl --user show probe.service --property=ActiveState \
      --property=CPUQuotaPerSecUSec --property=CPUWeight --property=MemoryHigh \
      --property=MemoryMax --property=MemoryCurrent --property=IOWeight \
      --property=TasksMax --property=TasksCurrent --property=CPUUsageNSec --no-pager

Name the file after the systemd version and record what it covers.
