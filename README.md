# Native Ingress Controller
This project provides a native ingress controller which can be deployed on OCI. The ingress module leverages
on the existing OCI load balancer features to act as an ingress controller for cluster services.

[![Go Report Card](https://goreportcard.com/badge/github.com/oracle/oci-native-ingress-controller)](https://goreportcard.com/report/github.com/oracle/oci-native-ingress-controller)
[![License](http://img.shields.io/badge/license-UPL%201.0-blue.svg)](https://oss.oracle.com/licenses/upl/)
[![Release](https://img.shields.io/github/v/release/oracle/oci-native-ingress-controller)](https://img.shields.io/github/v/release/oracle/oci-native-ingress-controller)
[![Coverage Status](https://coveralls.io/repos/github/oracle/oci-native-ingress-controller/badge.svg?branch=main)](https://coveralls.io/github/oracle/oci-native-ingress-controller?branch=main)

## Getting Started
Please have a look at [Getting Started](./GettingStarted.md)

## Backend repair

For over-limit OCI load balancers, `--backend-repair-only` runs one repair pass
over load balancers discovered from managed IngressClasses, without the ingress,
ingress-class, routing-policy, or certificate-cleanup controllers. The default
makes no OCI or leader-lease mutations: the controller logs each planned stale
registration and the projected LB-wide backend count. Add
`--backend-repair-apply` only after reviewing that plan.

Repair processes `default_ingress`-critical LBs first, then LBs with another
critical set, then warning and healthy LBs. For each LB it deletes registrations
in managed node-port sets only when their IP is absent from all Kubernetes
nodes. It preserves the final registration in each regular set. After cleanup,
it updates `default_ingress` only if both the current and projected LB-wide
counts fit OCI's 512-backend limit. A 429 or other OCI error stops the pass;
rerun it after inspecting the log and current LB state. This mode does not add
regular backends. Run a normal reconciliation after the repair and health check.
LBs without stale registrations or a critical default set are skipped.
Apply mode uses the configured leader lease so it cannot race the deployed
controller; keep the same lease name and namespace as the normal deployment.
The process releases the lease and exits when its repair pass finishes.

## Contributing

This project welcomes contributions from the community. Before submitting a pull
request, please [review our contribution guide](./CONTRIBUTING.md).

## Help

Project Developers: 
- Niranjan Pai ([@nirpai](https://github.com/nirpai))
- Inbaraj S ([@Inbaraj-S](https://github.com/Inbaraj-S))

## Security

Please consult the [security guide](./SECURITY.md) for our responsible security
vulnerability disclosure process.

## License

Copyright (c) 2023 Oracle and/or its affiliates.
Released under the Universal Permissive License v1.0 as shown at <https://oss.oracle.com/licenses/upl/>.
