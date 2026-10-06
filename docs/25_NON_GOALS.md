# Non-Goals

The following are explicitly outside v1 unless the owner changes the specification.

## Observability platform features
- Prometheus scraping
- PromQL
- OpenTelemetry
- tracing
- APM
- log aggregation
- log search
- metrics ingestion platform
- dashboards for arbitrary business metrics

## Infrastructure integrations
- Kubernetes controller/operator
- Docker socket monitoring
- SNMP
- cloud provider inventory
- service discovery

## Monitor breadth
- browser/synthetic Playwright checks
- MQTT
- database query checks
- game server checks
- Steam-specific checks
- custom JavaScript scripts
- arbitrary code execution
- plugin-defined monitors

## Distributed architecture
- remote generic probe agents
- multi-region coordinator
- leader election
- clustering
- horizontal scaling
- external queues

## SaaS/team features
- multi-tenancy
- organizations
- teams
- complex RBAC
- SAML
- OIDC
- billing
- subscriptions
- invitation workflow beyond simple viewer creation

## Extensibility
- plugin marketplace
- runtime plugin loading
- public SDK ecosystem
- generic event bus

## Frontend
- SPA requirement
- React/Vue/Svelte application shell
- client-side global state store
- WebSocket-first architecture

## Incident management
- on-call schedules
- escalation policies
- assignments
- incident commander
- investigate/identify/monitor workflow
- postmortem editor

## Release
- automatic self-update
- automatic release publishing
- automatic Docker image publishing

If a future feature needs one of these, update this file and the architecture specs before implementation.
