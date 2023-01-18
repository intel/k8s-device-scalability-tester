
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

Instructions below assume being done from the project root:
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

Instructions below assume being done from the deployments dir:
```
$ cd deployments/
```

In case you want to change in which namespaces test deployments run,
make sure to start from unmodified Git state:
```
$ git checkout .
```

Point deployment files to correct image registry / project for the images:
```
$ sed -i "s%image:.*/%image: $REGISTRY_URL/%" $(git ls-files '*.yaml')
```

Check that resulting image URLs are OK:
```
$ git grep image: '*.yaml'
```

If test cluster has Prometheus, but it is running in another namespace
than `monitoring`, change frontend + backend deployments and HPA rules
to use Prometheus namespace, so that its metrics access works:
```
sed -i 's/monitoring/<namespace>/' $(git ls-files '*.yaml')
```

If you want rest of deployments to run in some other namespace than
`validation`, change that too:
```
sed -i 's/validation/<namespace>/' $(git ls-files '*.yaml')
```

Check that there are no other namespaces used:
```
$ git grep -h 'namespace: ' '*.yaml' | sed 's/^[^:]*:/-/' | sort -u
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
[GPU Aware Scheduling](https://github.com/intel/platform-aware-scheduling/tree/master/gpu-aware-scheduling).

And device workloads like the media one, need a k8s persistent volume
with test data.

An example data volume is provided with the media backend for mounting
video files from an NFS server. Because different clusters offer
different types of volumes, you may need to use something else though.
See: https://kubernetes.io/docs/concepts/storage/volumes/

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

* If test cluster has nodes with different GPU capabilities, label
  suitable set of nodes, or use [labels provided by GPU plugin]
  (https://github.com/intel/intel-device-plugins-for-kubernetes/tree/main/cmd/gpu_nfdhook#introduction)
  or [Node Feature Discovery rules]
  (https://kubernetes-sigs.github.io/node-feature-discovery/stable/usage/features.html),
  and select them with backend `nodeSelector`

* Update workload arguments to match to your video file name, and set
  transcoding options (codec, scaling, format, number of frames)
  according to what you want to test, and how long each instance
  should run

* Check resource usage of the workload on the types of nodes where you
  intend to run the workloads, and update workload arguments or
  [backend deployment](media-queue/media-backend.yaml) k8s resource
  requests accordingly, to optimize workload scalability and its
  device utilization for your cluster HW, and what you want to test

  * For Intel GPU workloads, the relevant resources are
    `gpu.intel.com/millicores` and `gpu.intel.com/memory.max`

* Check how long test workload runs with given transcoding options. 
  To keep scaling validation rounds reasonably short, it's better to
  limit single workload run to few minutes or less.  If transcoding
  takes longer, shorten it by setting smaller transcoding frame count
  (with `-n` option for `sample_multi_transcode`).  Remember also to
  set deployment spec `terminationGracePeriodSeconds` to measures run
  lenght, to avoid deployment scale-in causing request interruptions
  i.e. failures

If you're not using e.g. XPU Manager + Prometheus + Grafana UI to
check GPU resource usage of the deployed workloads, you can do it also
manually from the command line:

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

Before running the validation, you need to deploy everything, in the
given order.

Create namespaces:
```
$ kubectl apply -f namespace/
```

Create frontend service:
```
$ kubectl apply -f frontend/service/
```

Deploy it:
```
$ kubectl apply -f frontend/frontend.yaml
```

Deploy "sleep" queue backend, client and its service + ingress:
```
$ kubectl apply -f sleep-queue/
```

Create data volume for "media" queue workloads (if not already created):
```
$ kubectl apply -f media-queue/volume/
```

Deploy "media" (transcode) queue backend, client and its service + ingress:
```
kubectl apply -f media-queue/
```

(If `apply` fails, try `delete` first.)


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
(Assumes default names for namespaces, and "sleep" backend taking 2s
for each request.)

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
time, so one can try device workloads with differing resource requests
being deployed and running in parallel.

To test this, make copies of the media media-queue deployment files
and change their backend transcode parameters to something taking
different amount of resources and lasting longer or shorter time, than
the original one.

Then rename their queues from "media" to something that better describes
what kind of operation the given backend instances do.  For example
"8bit-FullHD-HEVC-to-AVC-600-frames" and "10bit-4K-AV1-to-FullHD-HEVC".

After updating queue names in backend and client pod specs, add those
queue names also to frontend deployment arguments, and apply updated
frontend, backends and clients to cluster.

Just make sure you have enough GPU capacity available so that
scalability test script can scale all backends up to replica counts
you requested.


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
