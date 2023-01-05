Content
=======

* [Set up fuzzing](#set-up-fuzzing)
* [Fuzzing the frontend](#fuzzing-the-frontend)
* [Fuzzing the client](#fuzzing-the-client)


Set up fuzzing
==============

Install *radamsa* (on Fedora):
```
sudo dnf install radamsa
```

After that, one can use the supplied script to fuzz everything
automatically, using its defaults:
```
make
mkdir logs
fuzz/do-fuzz.sh
```

But it is better to build Golang memory sanitizer and race detector
versions of the framework binaries:
```
make msan race
```

And either tell the script to use those versions of the framework
binaries instead, or manually fuzz the tester frontend & client
network interfaces, as explained below.


Fuzzing the frontend
====================

This runs fuzzers at full tilt on all 3 frontend input ports.

Start frontend with race detector:
```
./tester-frontend-race -verbose fuzz1 fuzz2
```

Start 2 client queue push (request) input fuzzers:
```
radamsa -v -n inf -o 127.0.0.1:9997 fuzz/client1.json
radamsa -v -n inf -o 127.0.0.1:9997 fuzz/client2.json
```

Start fuzzing metric query input:
```
radamsa -v -n inf -o 127.0.0.1:9998 fuzz/metrics.http
```

Start 2 backend queue pull (reply) input fuzzers:
```
radamsa -v -n inf -o 127.0.0.1:9999 fuzz/backend1.json
radamsa -v -n inf -o 127.0.0.1:9999 fuzz/backend2.json
```

Stop after fuzzers show thousands of requests having been sent,
or frontend starts showing problems.


Fuzzing the client + frontend
=============================

Start frontend with memory sanitizer and two queues:
```
./tester-frontend-msan -verbose sleep fuzz1
```

Start real backend with memory sanitizer:
```
./tester-backend-msan -backoff 0.1 -backoff-max 0.2 sleep 0.2
```

Start real client with memory sanitizer:
```
./tester-client-msan -req-max 16 -req-now 4 -name sleep 0.2
```

Fuzz frontend client queue push (request) input:
```
radamsa -v -d 200 -n inf -o 127.0.0.1:9997 fuzz/client1.json
```

Fuzz client HTTP APIs:
```
radamsa -v -d 200 -n inf -o 127.0.0.1:9996 fuzz/client/*.http
```
