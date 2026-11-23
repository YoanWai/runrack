# Security

## Reporting

Please report vulnerabilities privately through GitHub: open the repository's **Security** tab and choose **Report a vulnerability**. Do not open a public issue for a security problem. You will get an answer within a week.

## What runrack touches

runrack runs with the privileges of the user who starts it. On a host with Incus that user is usually in `incus-admin`, which is root equivalent, and `incus exec` runs as root inside containers. That is why runrack keeps a strict boundary.

It reads:

- `/proc/<pid>/stat` for every process, and `cmdline` and `cgroup` only for processes named `Runner.Listener` or `Runner.Worker`
- `/proc/loadavg`, `/proc/stat`, `/proc/meminfo`, `/proc/uptime`
- `<runner>/.runner` (name, id, GitHub URL)
- `<runner>/_diag/Runner_*.log` lines with the version, architecture and configured labels
- `<runner>/_diag/Worker_*.log`: the first 2 MiB for the job message, and lines with step starts and the final result
- output of `systemctl list-units 'actions.runner.*'`, `systemctl show -p WorkingDirectory <runner unit>` and `incus list -f json`
- the GitHub REST API, only when a token is available, only `GET` requests

It never reads `.credentials`, `.credentials_rsaparams`, `.env` or `.path`, never reads a path containing `..`, and never runs `Runner.Listener` itself.

From the Worker log's job message it keeps the repository, workflow, run id, number and attempt, refs, event, actor, commit and server URL. The rest of the message, masked secrets included, is skipped by the JSON decoder. Nothing is written to disk.

## The command allowlist

Every command goes through `target.Check` in `internal/target/allowlist.go`. The allowed shapes are:

- `incus list -f json`
- `gh auth token`
- `[incus exec <c> --] systemctl list-units --all --plain --no-legend --no-pager actions.runner.*`
- `[incus exec <c> --] systemctl show -p WorkingDirectory actions.runner.<name>.service`
- `incus exec <c> -- head -c <N> <path>`
- `incus exec <c> -- ls -1 <dir>`
- `incus exec <c> -- grep -a -h -F -e <fixed string>... <path>` with strings from a constant list

No shell is ever involved, and nothing that starts, stops or changes a runner, unit or container is allowed. Tests fail if any other code path starts a process.
