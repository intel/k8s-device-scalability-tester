
Device Scalability Tester for Kubernetes
========================================

This is a Go based Kubernetes (k8s) device scalability testing
framework. It is intended for pre-production testing done in *secure /
non-production* test clusters.

It provides test backend, frontend and client services, plus a script
for validating expected device / service scalability of workload(s)
configured for the backend.

Frontend provides Prometheus metrics both for for monitoring the
scalability.  Client provides statistics of request throughput, and which
nodes / devices were used by the backend pod instances, to perform
those requests.

Use-cases:
* Automation for testing real k8s workload parallelization throughput on given device HW
* Automation for stress testing devices and device drivers on multiple nodes in parallel
* Demonstrating k8s device usage scaling of real use-cases using live Grafana graphs
* Testing k8s HPA with application metrics (custom metrics became stable in k8s v1.23):
  https://kubernetes.io/docs/tasks/run-application/horizontal-pod-autoscale/#scaling-on-custom-metrics
* Validating that GAS and fractional device resource usage works correctly with scaling:
  https://github.com/intel/intel-device-plugins-for-kubernetes/tree/main/cmd/gpu_plugin#fractional-resources

See:
* [Design document](docs/README.md)

Links:
* [Intel device plugins for Kubernetes](https://github.com/intel/intel-device-plugins-for-kubernetes/)
* [Simulated device metrics](https://github.com/intel/fakedev-exporter/)
