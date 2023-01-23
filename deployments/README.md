
Scalability testing
===================

Contents:
* [Introduction](#introduction)
* [Deployment description](#deployment-description)
  * [Frontend service](#frontend-service)
  * [Example workloads](#example-workloads)
  * [Client services](#client-services)
* [Basic deployment setup](#basic-deployment-setup)
  * [Build containers](#build-containers)
  * [Configure deployments](#configure-deployments)
* [Device scalability testing setup](#device-scalability-testing-setup)
  * [Configure test cluster](#configure-test-cluster)
  * [Configure device deployments](#configure-device-deployments)
  * [Measure GPU resource usage](#measure-gpu-resource-usage)
* [Scalability validation](#scalability-validation)
  * [What validation does](#what-validation-does)
  * [Validation setup](#validation-setup)
  * [Running the validation](#running-the-validation)
  * [Using multiple queues in parallel](#using-multiple-queues-in-parallel)
  * [Cascade scaling and parallel encoding](#cascade-scaling-and-parallel-encoding)
* [Kubernetes scaling strategies](#kubernetes-scaling-strategies)
  * [Manual scaling](#manual-scaling)
  * [Scaling with HPA](#scaling-with-hpa)
  * [Scaling with CronJobs](#scaling-with-cronjobs)

TODO:
* Add helper scripts for basic deployment configuration, and for
  starting all the deployments


Introduction
============

Before doing the service scalability testing, you need to build the
test containers used by the services, and configure their deployments
to match your test cluster.

Project Kubernetes integration includes:
* Container Docker files and corresponding Pod YAML files
  * Both for the framework itself, and example workload(s)
* Prometheus integration for the workqueue metrics (provided by the frontend)
* Example HPA rules for workqueue metric based Horizontal Pod Autoscaling
* Grafana view for monitoring work queue metrics

Quick start (with "sleep" backend):
* [Basic deployment setup](#basic-deployment-setup)
* [Scalability validation](#scalability-validation)


Deployment description
======================

Frontend service
----------------

Frontend service deployment provides ports where scalability test
clients, workloads and monitoring can connect to.

Spec files:
* [Frontend deployment](frontend/frontend.yaml)
* [Frontend service](frontend/service/):
  - Service (ports names & numbers)
  - (Prometheus) service monitor
  - Cluster role
  - Service account
  - Binding role to service
* [Queue metrics dashboard for Grafana](grafana/dashboard.json)


Example workloads
-----------------

Test framework has built-in backend "sleep" workload which can be used
to verify that the framework itself works fine, before moving to
testing of real workloads.

(Validation script should show (near) 2.0 scaling on each backend
replica count doubling for the "sleep" backend.)

Spec files for the example workloads:
* Sleep
  - [Backend deployment](sleep-queue/sleep-backend.yaml)
* Intel GPU media transcoding:
  - [Container](../scalability-tester-media.docker)
  - [Backend data volume](media-queue/volume/data-volume.yaml)
  - [Backend deployment](media-queue/media-backend.yaml)


Client services
---------------

Each test workload queue has a client sending it constantly requests,
and providing a HTTP service that allows both configuring the request
parallism, and getting different statistics on request results.

There are also Ingress configurations for each client HTTP service, so
that this can be done from outside the test cluster if/when needed.

* Sleep
  - [Client deployment](sleep-queue/sleep-client.yaml)
  - [Client ingress](sleep-queue/sleep-client-ingress.yaml)
* Intel GPU media transcoding:
  - [Client deployment](media-queue/media-client.yaml)
  - [Client ingress](media-queue/media-client-ingress.yaml)


Basic deployment setup
======================

Build containers
----------------

Instructions below assume being done from the project root directory:
```
$ cd <project root>
```

And variable below pointing to image registry / project where you
can push the built image:
```
$ REGISTRY_URL=<registry>/<project>
```

Build the test containers:
```
$ docker build --rm -t $REGISTRY_URL/k8s-device-scalability-tester:latest \
  -f scalability-tester.docker .
$ docker build --rm -t $REGISTRY_URL/k8s-device-scalability-tester-media:latest \
  -f scalability-tester-media.docker .
```

Push them to your local registry:
```
$ docker push $REGISTRY_URL/k8s-device-scalability-tester:latest
$ docker push $REGISTRY_URL/k8s-device-scalability-tester-media:latest
```


Configure deployments
---------------------

Change deployments to use images from correct registry, and run in the
cluster namespaces you specify.

For example, if Prometheus runs `monitoring` namespace, and other
tester deployments (which Prometheus does not need to query) should
run in `validation` namespace, use:
```
./deployments-config.sh $REGISTRY_URL monitoring validation
```


Device scalability testing setup
================================

Because test clusters can have different volumes, (GPU) HW and
addresses available for them, scalability testing device workloads
need quite a bit of configuration.


Configure test cluster
----------------------

Sharing Intel GPUs based on
[fractional pod allocation requests](https://github.com/intel/intel-device-plugins-for-kubernetes/blob/main/cmd/gpu_plugin/README.md#install-to-nodes-with-intel-gpus-with-fractional-resources)
requires, in addition to GPU plugin, cluster to be running with
[GPU Aware Scheduling](https://github.com/intel/platform-aware-scheduling/tree/master/gpu-aware-scheduling)
(GAS).

And device workloads like the media one, need a k8s persistent volume
with test data.

An example data volume is provided with the media backend for mounting
video files from an NFS server. Because different clusters offer
different types of volumes, you may need to use something else though.
See [Kubernetes volumes](https://kubernetes.io/docs/concepts/storage/volumes/).

After volume is there, add to it few media files which transcoding
scalability you would like to test in k8s with the media backend.

Note that included oneVPL tool does not support media containers, so
test files should be just the (av1, h264, h265, mpeg2, vp9...) video
content. FFmpeg can extract video content from
[media containers](https://en.wikipedia.org/wiki/Comparison_of_video_container_formats).

(If e.g. `ffprobe` tells video track in `video.mp4` file to be in
H.264 encoding, `ffmpeg -i video.mp4 -an -c:v copy video.h264`
extracts the video content as-is.)

While it is out of scope for this project, scalability testing without
monitoring would be a poor excercise. Standard Prometheus and Grafana
components can be installed (e.g. with Helm charts) to monitor queue
metrics provided by the frontend, and XPU manager can provide Intel
GPU utilization metrics to Prometheus:
https://github.com/intel/xpumanager


Configure device deployments
----------------------------

Deployments for the device workloads need to be configured for your
particular cluster setup, and HW available there:

* Update the [backend data volume](media-queue/volume/data-volume.yaml) spec file
  to match volume you have reserved for the test videos

  * Alternatively, one could add test content directly to the
    container. That can be easier to start with, but when number of
    test backends and test files grows, separate volume makes managing
    sharing and updating them easier

* If test cluster has nodes with different GPU capabilities, label suitable set of nodes, or use
  [labels provided by GPU plugin](https://github.com/intel/intel-device-plugins-for-kubernetes/tree/main/cmd/gpu_nfdhook#introduction)
  or
  [Node Feature Discovery rules](https://kubernetes-sigs.github.io/node-feature-discovery/stable/usage/features.html),
  and select them with backend `nodeSelector`

* Update workload arguments to match to your video file name, and set
  transcoding options (codec, scaling, format, number of frames)
  according to what you want to test, and how long each instance
  should run

* Measure resource usage of the workload on the types of nodes where
  you intend to run the workload instances, and either update workload
  arguments, or [backend deployment](media-queue/media-backend.yaml)
  k8s device resource requests accordingly, to optimize workload
  scalability and its device utilization for your cluster HW, and what
  you want to test

  * For Intel GPU workloads, most relevant resource is
    `gpu.intel.com/memory.max`, followed by `gpu.intel.com/millicores`.
    For their values, see [Measure GPU resource usage](#measure-gpu-resource-usage).

    Telling how much GPU memory workload uses at maximum, avoids GPU
    memory being oversubscribed, which could result in significant
    slowdown (due to paging), and potential workload termination.

    Millicores value can be used to limit device sharing further, if
    given workload would need larger share of the GPU time to complete
    in acceptable time frame. Suitable value depends on capacity of
    the GPU on which the workload is run on, not just the workload
    itself (like memory usage does)

* Check how long test workload runs with given transcoding options. 
  To keep scaling validation rounds reasonably short, it's better to
  limit single workload run to few minutes or less.  If transcoding
  takes longer, shorten it by setting smaller transcoding frame count
  (with `-n` option for `sample_multi_transcode`).  Remember also to
  set deployment spec `terminationGracePeriodSeconds` to measures run
  lenght, to avoid deployment scale-in causing request interruptions
  i.e. failures

* "media" backend deployment spec uses user ID 0 (root), to make sure it can
  access k8s provided device file regardless of what is installed on the GPU node host.
  [Non-root device containers](https://kubernetes.io/blog/2021/11/09/non-root-containers-and-devices/)
  tells what is needed from cluster to run device containers under other user IDs


Measure GPU resource usage
--------------------------

If cluster already runs XPU Manager + Prometheus + Grafana UI, it is
easiest to check GPU resource usage of the deployed workloads from
a Grafana GPU metrics dashboard.

But you can do it also manually from the command line:

  * Run the workload directly (from a dir with test content) *with same
    arguments that the deployment uses*, with something like this:
    ```
    $ sudo docker run -it --rm \
      --volume $PWD:/media:ro \
      --device /dev/dri:/dev/dri:rw \
      --user root --network none --cap-drop ALL \
      $REGISTRY_URL/k8s-scalability-tester-media:latest \
      sample_multi_transcode -i::h265 /media/video.h265 -o::h264 /dev/null -async 4
    ```

  * Monitor Intel GPU engine utilization with `intel_gpu_top` while running
    media backend, using `-d` option to specify the same GPU as what the
    workload is using:
    ```
    $ sudo bash
    # dnf install igt-gpu-tools     # on Fedora
    # apt install intel-gpu-tools   # on Debian/Ubuntu
    # GPU=card0
    # sudo intel_gpu_top -d drm:/dev/dri/$GPU
    ```

  * Monitor Intel GPU memory usage for the same GPU in another terminal:
    ```
    $ sudo bash
    # GPU=card0
    # cd /sys/class/drm/$GPU/clients/
    # watch head */name */*/created_bytes
    ```


Scalability validation
======================

[Scalability validation script](../validate-scaling.sh) verifies that
(the already running) scalability testing setup provides (at least)
specified client throughput increase, each time backend deployment
replica count is doubled.


What validation does
--------------------

How the script works:
* Prepare for scaling validation:
  * Disable client requests
  * Verify that specified max number of backends can be deployed
  * Delete all backends
  * Set initial scale to 1
* Scaling validation loop (runs until max specified scale is reached):
  * Set backend deployment replicas and client request parallelization to scale
  * Reset client metrics and wait for request to be processed
  * Fetch resulting client statistics
  * Verify that:
    * There were no request handling errors
    * Throughput increased at least by specified amount (after scale doubling)
  * Doubles scale
* Finally:
  * Show client node/device distribution info


Validation setup
----------------

Before running the validation, you need to deploy everything to the
cluster, in the correct order.  To do that, copy `deployments/`
directory content along with the `deployments-apply.sh`, to a host
with `kubectl` having access to you cluster API server.

When run, script removes pre-existing scalability tester deployment
from the cluster, and starts it again with only the specified test
queues. Following would deploy scalability tester with both "sleep"
and "media" test queues:
```
$ deployments-apply.sh sleep media
```

If you just want to delete the whole scalability tester deployment, do
not specify any queue names. Add `-v` option if you want also data
volume(s) for the test queue(s) to be deleted:
```
$ deployments-apply.sh -v
```


Running the validation
----------------------

Validate scalability framework to scale, up to 16 replicas, with at
least 1.8x request throughput increase on each backend replica count
doubling, when using the (builtin) "sleep" backend / queue:
```
$ ./validate-scaling.sh scalability-tester-backend-sleep monitoring 2 scalability-tester-client-sleep validation 16 1.8
...
Throughput: 3.492229 -> 6.841997 = 1.95 increase on reqs-per-sec
(pods: 16)
...
*** PASS ***
```
Above assumes default names for the namespaces, and "sleep" backend
being configured to take 2s for each request.  For more accuracy, a
significantly larger runtime value than what the backend actually
uses, should be specified.

(To reduced testing time, script runs each test round only 4x longer
than the specified request time, which does not provide very accurate
results.)

Validate media backend scalability to have at least 1.4x throughput
increase for each backend replica count doubling, for workload running
for 30s, in a cluster where deployment can be scaled to 8 replicas
with the specified GPU resource usage (e.g. workload taking 50% of the
GPU, and cluster having 2 matching nodes with 2 GPUs each):
```
$ ./validate-scaling.sh scalability-tester-backend-media monitoring 30 scalability-tester-client-media validation 8 1.4
```

(It's better to start with short workloads and low scalability
expectations until specified workload node selector correctness has
been verified and backend spec resource requests have been optimized
for the HW on the matching nodes.)


Using multiple queues in parallel
---------------------------------

Test framework supports multiple test queues being used at the same
time, so one can try device workloads with different resource usages
and running times, being deployed and running in parallel.

To test this, make copy of the `media-queue` deployment. Give the new
queue a name that describe its intended load well (better than
"media"). For example "10bit-4K-AV1-to-FullHD-HEVC".

Because `deployments-apply.sh` and `validate-scaling.sh` scripts
expect deployments directory, spec files, and k8s object names to
correspond to the queue name, use the provided script to create
new deployment files from a suitable existing one, like this:
```
$ ./deployments-backend.sh media 10bit-4K-AV1-to-FullHD-HEVC
```

Change the new (`<name>-queue/<name>-backend.yaml`) backend spec file:

* Transcode parameters to something matching the queue name

* Resources limits to match how much resources backend takes with those parameters

* Pod grace period to match how long backend instance runs

(See [Configure device deployments](#configure-device-deployments) for
more information.)

And add the new queue name(s) also to frontend deployment arguments
(in `frontend/frontend.yaml`), so that it accepts the new queue client
and backend connections.

Finally, apply the new configuration to the cluster with `deployments-apply.sh`.

For validation still to pass when all the queues are being tested /
running at the same time, make also sure to have enough GPU capacity
to scale backends for all queues up to desired replica counts.


Cascade scaling and parallel encoding
-------------------------------------

Intel OneVPL trascoding tool in the media backend supports also more
advanced transcoding options, such as "cascade scaling" (transcoding
video to multiple different formats in one go), and "parallel
encoding" (splitting encoding part of transcoding to multiple GPUs).

OneVPL project provides tooling to investigate in which situations
those improve performance.

For details, see: https://github.com/oneapi-src/oneVPL/tree/master/doc/


Kubernetes scaling strategies
=============================

There are several alternatives for scaling backend pod instances.

Note: When backend runs as HPA controlled deployment, its `-backoff`
option should be used to avoid k8s reporting "crash" restarts.  If
backend is run as CronJob with no scale-down strategy, its default
policy (exit if queue becomes empty) should be used instead.

See [the design document](../docs/README.md).


Manual scaling
--------------

Modify backend pod replica count directly:
```
kubectl scale -n <namespace> deployment/<backend> --replicas=<count>
```

Like the included scalability validation script does.


Scaling with HPA
----------------

Kubernetes Horizontal Pod Autoscaling (HPA) rules can be used to
control backend worker replica count based on suitable metrics.
Frontend provides per-queue metrics for this, such as number of items
waiting to be processed.

Deployments folder contains HPA examples for the `sleep` queue,
both for using k8s [custom metrics](sleep-queue/scaling-custom/)
and [external metrics](sleep-queue/scaling-external).

In my own testing (for requests which may take minutes to finish),
HPA rules either do not act fast enough (when evaluation period is
longer), or cause large replica count fluctuations (when period is
shorter), so it seems more suitable for backends which setup and
teardown are very lightweight.


Scaling with CronJobs
---------------------

Scaling can be done also with a k8s CronJob that starts additional
backend instances at fixed interval. Backend deployments should then
be configured to exit when their queue empties (`-backoff 0`).
