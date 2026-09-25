# Launch kit

This file contains copy and metadata suggestions for announcing StePanel.

## Suggested GitHub topics

`hosting-panel`, `server-management`, `linux-hosting`, `cPanel-migration`, `cpmove`, `mysql`, `mariadb`, `postgresql`, `phpmyadmin`, `phppgadmin`, `apache`, `caddy`, `php`, `nodejs`, `golang`, `self-hosted`, `devops`, `system-administration`, `backup-restore`

## Short description

Safety-first Linux web and application hosting control plane with cPanel migration tooling.

## Launch post

StePanel is an open-source, Go-based control plane for small Linux web and application hosting fleets. It installs Caddy by default with Apache/OpenLiteSpeed options, supports PHP and application runtimes, validates cPanel cpmove archives, stages migrations safely, and exposes restore jobs through a focused operator dashboard.

Repository: https://github.com/cyberducttape/StePanel

## Longer description

StePanel is built for operators who want a clear, inspectable migration path away from cPanel. The project combines a small Go control plane with Caddy-first web/application hosting, explicit systemd service isolation, archive safety checks, asynchronous restore jobs, audit events, health endpoints, and reproducible release artifacts.

## Before promoting publicly

- Publish a tagged release and GitHub Release notes.
- Add real dashboard screenshots or a short GIF.
- Set the repository description and topics.
- Enable Discussions if community support is desired.
- Add a demo server only if it is isolated and disposable.
- Link to a security contact and support policy.
