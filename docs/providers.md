# Provider notes

runrack ships with the GitHub Actions provider. These notes collect what other runners expose locally, so a provider can follow the same rule: find the runner from processes and units, read job details from what it already writes, and never change anything.

## GitHub Actions (shipped)

- Processes: `Runner.Listener run` is the runner, a `Runner.Worker` child is the running job.
- Unit: `actions.runner.<scope>.<name>.service`, `WorkingDirectory` is the runner root.
- Identity: `<root>/.runner` (JSON with a UTF-8 BOM): `agentName`, `agentId`, `gitHubUrl`.
- Jobs: `<root>/_diag/Worker_YYYYMMDD-HHMMSS-utc.log`. The `Job message:` JSON has the job name and `contextData.github`. Steps are `StepsRunner] Processing step: DisplayName='...'` lines and the result is `Job result after all job steps finish: Succeeded|Failed|Canceled`.
- API: `/orgs/{org}/actions/runners`, `/repos/{repo}/actions/runs/{id}/attempts/{n}/jobs`.

## GitLab Runner

- Process `gitlab-runner run`, unit `gitlab-runner.service`, config `/etc/gitlab-runner/config.toml` (contains tokens, never read it whole).
- Docker executor job containers carry labels `com.gitlab.gitlab-runner.job.id`, `.job.ref`, `.project.id`, `.pipeline.id`.
- With `listen_address` set, `/debug/jobs/list` lists running jobs.

## Forgejo and Gitea act_runner

- Process `act_runner daemon`, a `.runner` JSON file next to the config with the instance URL and runner name.
- Job containers are named `GITEA-ACTIONS-TASK-<id>_...` or `FORGEJO-ACTIONS-TASK-<id>_...`. Forgejo and Gitea drift apart, so they should be separate providers.

## Woodpecker agent

- Process `woodpecker-agent`. With the health server on (default `:3000`), `GET /varz` reports running workflows.

## Buildkite agent

- Process `buildkite-agent start`. With `--health-check-addr`, `/status.json` lists the agent's state and current job.

## Jenkins agent

- A JVM running `agent.jar` or `remoting.jar`. Job details live in the child process environment (`JOB_NAME`, `BUILD_NUMBER`, `BRANCH_NAME`).
- The agent command line holds the secret: argv must be redacted before it is shown or logged.

## Azure Pipelines agent

- Same code base as the GitHub runner: `Agent.Listener` and `Agent.Worker`, units `vsts.agent.*`, logs in `_diag/Agent_*.log` and `_diag/Worker_*.log`. The GitHub parsers should carry over with different names.
