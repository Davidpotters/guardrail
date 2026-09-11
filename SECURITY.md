# Security Policy

This is a personal portfolio project, not a maintained production tool --
there's no bug bounty program and no guaranteed response time. That said,
if you find a real vulnerability, I'd genuinely like to know about it.

## Reporting a vulnerability

Please don't open a public GitHub issue for a security finding. Instead,
reach out through the contact info on
[davidpottersdev.com](https://davidpottersdev.com/contact).

Include what you found, how to reproduce it, and its potential impact if
you can. I'll acknowledge reports as soon as I see them and fix real
issues as time allows.

## Scope

This covers the code in this repository -- the Go admission webhook, the
CI pipeline, and the Kubernetes manifests. It does not cover the local
`kind` cluster setup itself, which is a local development environment,
not a deployed service.
