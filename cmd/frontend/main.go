// Copyright 2023 Intel Corporation
// SPDX-License-Identifier: Apache-2.0
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

const (
	project   = "Device Scalability Tester for Kubernetes - frontend"
	version   = "v0.1"
	tcpSize   = 1024 // max space for a TCP message
	metricURL = "/metrics"
)

var (
	verbose bool
)

// client service request.
type clientReq struct {
	Queue string   // queue name
	Args  []string // extra workload arguments
	Limit float64  // workload run-time limit, in secs (0=default)
}

// worker work item request.
type workReq struct {
	Queue string // queue name
}

// work for worker.
type workItem struct {
	Error string   // non-empty on errors
	Args  []string // extra workload arguments
	Limit float64  // in secs (0=default)
	Empty bool     // true if error is due to queue being empty
}

type queueItem struct {
	// where to reply
	client net.Conn
	// when pulled - when added = wait time
	added time.Time
	// marshaled to request
	Args  []string
	Limit float64
}

type queueT struct {
	// queue of items waiting to be processed
	items []queueItem
	// number of items being processed
	running int
	// queue processing statistics
	disconnect uint64
	success    uint64
	failure    uint64
	// queue processing timings
	maxtotal float64
	maxwait  float64
	maxrun   float64
	// locking for those
	mutex sync.Mutex
}

// type is needed to be able to have methods for queues, and
// queueT needs to be pointer as address cannot be taken for map index.
// maps is created and interval set at startup, they are not modified
// after goroutines are created and therefore do not need locking.
type queuesT struct {
	maps map[string]*queueT
	// connection counters
	clients uint64
	workers uint64
	metrics uint64
	// locking for connection counters
	mutex    sync.Mutex
	interval int // >0 to enable stats logging (secs)
}

// worker -> frontend -> client return replies.
type replyT struct {
	Pod      string  // who did work
	Node     string  // on which node
	Device   string  // device mapped to worker, if any
	Error    string  // non-empty on errors
	Timeout  float64 // >0 = workload timed out
	Runtime  float64 // workload run time, in secs
	Waittime float64 // queue wait time, in secs, added by frontend
	Retcode  int     // workload return code
}

func requestCheck(r *http.Request) int {
	if r.Method != http.MethodGet {
		return http.StatusMethodNotAllowed
	}

	if r.URL.Path != metricURL {
		return http.StatusNotFound
	}

	if r.Body != http.NoBody {
		return http.StatusBadRequest
	}

	return http.StatusOK
}

// exporter reports queue statistics as Prometheus metrics,
// resets max times on each query unless stats logging + reset
// interval is enabled.
func (queues *queuesT) exporter(w http.ResponseWriter, r *http.Request) {
	if status := requestCheck(r); status != http.StatusOK {
		w.WriteHeader(status)
		return
	}

	if verbose {
		log.Printf("metrics query from '%s'", r.RemoteAddr)
	}
	// report results and reset max timer
	fmt.Fprintf(w, "# %s %s\n", project, version)

	queues.mutex.Lock()
	fmt.Fprintf(w, "hpa_client_connections_total %d\n", queues.clients)
	fmt.Fprintf(w, "hpa_worker_connections_total %d\n", queues.workers)
	queues.metrics++
	queues.mutex.Unlock()

	for name, q := range queues.maps {
		q.mutex.Lock()

		fmt.Fprintf(w, "hpa_queue_all{name=\"%s\"} %d\n", name, len(q.items)+q.running)
		fmt.Fprintf(w, "hpa_queue_waiting{name=\"%s\"} %d\n", name, len(q.items))
		fmt.Fprintf(w, "hpa_queue_running{name=\"%s\"} %d\n", name, q.running)
		fmt.Fprintf(w, "hpa_queue_success_total{name=\"%s\"} %d\n", name, q.success)
		fmt.Fprintf(w, "hpa_queue_failure_total{name=\"%s\"} %d\n", name, q.failure)
		fmt.Fprintf(w, "hpa_queue_disconnect_total{name=\"%s\"} %d\n", name, q.disconnect)

		if queues.interval > 0 {
			labels := fmt.Sprintf("name=\"%s\",interval=\"%ds\"", name, queues.interval)
			fmt.Fprintf(w, "hpa_queue_maxrun_seconds{%s} %g\n", labels, q.maxrun)
			fmt.Fprintf(w, "hpa_queue_maxwait_seconds{%s} %g\n", labels, q.maxwait)
			fmt.Fprintf(w, "hpa_queue_maxtotal_seconds{%s} %g\n", labels, q.maxtotal)
		} else {
			fmt.Fprintf(w, "hpa_queue_maxrun_seconds{name=\"%s\"} %g\n", name, q.maxrun)
			fmt.Fprintf(w, "hpa_queue_maxwait_seconds{name=\"%s\"} %g\n", name, q.maxwait)
			fmt.Fprintf(w, "hpa_queue_maxtotal_seconds{name=\"%s\"} %g\n", name, q.maxtotal)

			q.maxrun, q.maxwait, q.maxtotal = 0, 0, 0
		}

		q.mutex.Unlock()
	}
}

// logStats logs queue statistics at specified interval, resets max times after each output.
func (queues *queuesT) logStats() {
	log.Printf("Logging queue statistics at %ds interval", queues.interval)
	duration := time.Duration(queues.interval) * time.Second

	for {
		time.Sleep(duration)

		for name, q := range queues.maps {
			q.mutex.Lock()

			log.Printf("%s: %d backend successes, %d failures - %d still running (max %.2fs), %d waiting (max %.1fs) in queue (with max total %.1fs) - %d client disconnects",
				name, q.success, q.failure, q.running, q.maxrun, len(q.items), q.maxwait, q.maxtotal, q.disconnect)

			q.maxrun, q.maxwait, q.maxtotal = 0, 0, 0

			q.mutex.Unlock()
		}

		queues.mutex.Lock()
		log.Printf("%d client, %d metric and %d worker connections in total", queues.clients, queues.metrics, queues.workers)
		queues.mutex.Unlock()
	}
}

// sendJSONClose sends given data and closes connnection.
func sendJSONClose(conn net.Conn, data []byte) {
	if verbose {
		log.Printf("Closing reply (%d bytes): %v", len(data), string(data))
	}

	n, err := conn.Write(data)
	if err != nil || n != len(data) {
		log.Printf("WARN: JSON msg write (%d/%d bytes) failed: %v", n, len(data), err)
	}

	conn.Close()
}

// errorReplyClose marshals + sends given error reply, and closes connection.
func errorReplyClose(conn net.Conn, msg string) {
	reply := replyT{Error: msg, Retcode: 1}
	data, err := json.MarshalIndent(reply, "", "\t")

	if err != nil {
		log.Fatalf("ERROR: internal replyT JSON marshaling error: %v", err)
	}

	sendJSONClose(conn, data)
}

// listenForClients accepts client connections, validates the service request,
// and either returns an error, or adds the request to specified queue with
// the connection needed to return the data.
func (queues *queuesT) listenForClients(address string, qmax int) {
	log.Printf("Queueing client service request work items on %s", address)

	l, err := net.Listen("tcp", address)
	if err != nil {
		log.Fatalf("Listening on '%s' failed: %v", address, err)
	}

	data := make([]byte, tcpSize)

	for {
		if verbose {
			log.Print("Accepting next client...")
		}

		conn, err := l.Accept()
		if err != nil {
			log.Printf("Accept for '%s' failed: %v", address, err)
			continue
		}

		queues.mutex.Lock()
		queues.clients++
		queues.mutex.Unlock()

		if verbose {
			log.Print("Reading client request...")
		}

		var n int
		n, err = conn.Read(data)

		if n <= 0 || err != nil {
			errorReplyClose(conn, fmt.Sprintf("No bytes (%d) / error (%s) from read", n, err))
			continue
		}

		if verbose {
			log.Printf("Service request (%d bytes): %v", n, string(data))
		}

		var req clientReq
		if err = json.Unmarshal(data[:n], &req); err != nil {
			errorReplyClose(conn, fmt.Sprintf("Unmarshaling clientReq JSON failed: %v", err))
			continue
		}

		name := req.Queue
		if name == "" {
			errorReplyClose(conn, "invalid queue name ''")
			continue
		}

		if _, exists := queues.maps[name]; !exists {
			errorReplyClose(conn, fmt.Sprintf("Unknown '%s' queue", name))
			continue
		}

		queue := queues.maps[name]

		queue.mutex.Lock()
		if qmax > 0 && len(queue.items) >= qmax {
			errorReplyClose(conn, fmt.Sprintf("'%s' queue already at full capacity (%d)", name, qmax))
		} else {
			// all OK, add to queue
			item := queueItem{
				Limit:  req.Limit,
				Args:   req.Args,
				added:  time.Now(),
				client: conn,
			}
			queue.items = append(queue.items, item)
		}
		queue.mutex.Unlock()
	}
}

// processItem sends given item to worker and waits for reply.
// Worker reply (or failure) info is sent back to client, both client
// and worker connections are closed, and processing info returned.
func processItem(worker net.Conn, item queueItem) replyT {
	// marshal work item
	workitem := workItem{
		Limit: item.Limit,
		Args:  item.Args,
	}

	data, err := json.MarshalIndent(workitem, "", "\t")
	if err != nil {
		log.Fatalf("ERROR: internal workItem error JSON marshaling error: %v", err)
	}

	if verbose {
		log.Printf("Work item to worker (%d bytes): %v", len(data), string(data))
	}

	queuetime := time.Since(item.added).Seconds()
	reply := replyT{
		Waittime: queuetime,
		Retcode:  1,
	}

	// send the item and wait for reply
	n, err := worker.Write(data)
	if err != nil || n != len(data) {
		errorReplyClose(item.client, fmt.Sprintf("Worker write (%d/%d bytes) failed: %v\n", n, len(data), err))
		worker.Close()

		return reply
	}

	data = make([]byte, tcpSize)
	n, err = worker.Read(data)
	worker.Close()

	if n <= 0 || err != nil {
		errorReplyClose(item.client, fmt.Sprintf("No bytes (%d) / error (%v) from worker reply read", n, err))
		return reply
	}

	if verbose {
		log.Printf("Worker reply (%d bytes): %v", n, string(data))
	}

	// unmarshal reply, add queueing time back to it, remarshal and send it
	err = json.Unmarshal(data[:n], &reply)
	reply.Waittime = queuetime

	if err != nil {
		errorReplyClose(item.client, fmt.Sprintf("Unmarshaling worker reply JSON failed: %v", err))

		reply.Retcode = 1

		return reply
	}

	data, err = json.MarshalIndent(reply, "", "\t")
	if err != nil {
		log.Fatalf("ERROR: internal replyT JSON marshaling error: %v", err)
	}

	sendJSONClose(item.client, data)

	return reply
}

// doItem checks that client for the work item is still there, processes it
// and updates queue statistics accordingly after work item reply is completed.
func doItem(worker net.Conn, item queueItem, queue *queueT) {
	reply := processItem(worker, item)

	queue.mutex.Lock()
	defer queue.mutex.Unlock()

	if reply.Waittime > queue.maxwait {
		queue.maxwait = reply.Waittime
	}

	if reply.Runtime > queue.maxrun {
		queue.maxrun = reply.Runtime
	}

	total := reply.Waittime + reply.Runtime
	if total > queue.maxtotal {
		queue.maxtotal = total
	}

	if reply.Retcode == 0 {
		queue.success++
	} else {
		queue.failure++
	}
	queue.running--
}

// errorItemClose marshals + sends given error item, and closes connection.
func errorItemClose(conn net.Conn, empty bool, msg string) {
	reply := workItem{Error: msg, Empty: empty}

	data, err := json.MarshalIndent(reply, "", "\t")
	if err != nil {
		log.Fatalf("ERROR: internal workItem error JSON marshaling error: %v", err)
	}

	sendJSONClose(conn, data)
}

// countObsolete returns number of items (at queue front) for
// which client has disconnected (and closes their connections).
func countObsolete(queue *queueT) int {
	count := 0
	buf := make([]byte, 1)

	for _, item := range queue.items {
		// TODO:annoyingly, for disconnect detection to work,
		// it must try non-zero sized read with deadline in
		// *future* i.e. this adds delay to request processing
		//
		// Unix module would allow checking connection state
		// (extract its FD, use syscalls to check the state),
		// but it's not in Golang standard library
		if err := item.client.SetReadDeadline(time.Now().Add(time.Millisecond)); err != nil {
			log.Fatalf("ERROR: failed to set read deadline: %v", err)
		}

		_, err := item.client.Read(buf)
		if errors.Is(err, os.ErrDeadlineExceeded) {
			// conn still OK, disable deadline
			if err := item.client.SetReadDeadline(time.Time{}); err != nil {
				log.Fatalf("ERROR: failed to remove read deadline: %v", err)
			}
			// check next queue item
			break
		}
		// client gone -> close
		item.client.Close()
		count++
	}

	return count
}

// listenForWorkers accepts worker connections, validates the queue item
// pull request, and either returns an error, or provides the first item
// and starts a goroutine sending reply, waiting for a reply and closing
// connection.
func (queues *queuesT) listenForWorkers(address string) {
	log.Printf("Providing queued work items for backends on %s", address)

	l, err := net.Listen("tcp", address)
	if err != nil {
		log.Fatalf("Listening on '%s' failed: %v", address, err)
	}

	data := make([]byte, tcpSize)

	for {
		if verbose {
			log.Print("Accepting next worker...")
		}

		conn, err := l.Accept()
		if err != nil {
			log.Printf("Accept for '%s' failed: %v", address, err)
			continue
		}

		queues.mutex.Lock()
		queues.workers++
		queues.mutex.Unlock()

		if verbose {
			log.Print("Reading worker spec...")
		}

		var n int

		n, err = conn.Read(data)
		if n <= 0 || err != nil {
			errorItemClose(conn, false, fmt.Sprintf("No bytes (%d) / error (%v) from read", n, err))
			continue
		}

		if verbose {
			log.Printf("Work item request (%d bytes): %v", n, string(data))
		}

		var req workReq
		if err = json.Unmarshal(data[:n], &req); err != nil {
			errorItemClose(conn, false, fmt.Sprintf("Unmarshaling workReq JSON failed: %v", err))
			continue
		}

		name := req.Queue
		if name == "" {
			errorItemClose(conn, false, "invalid queue name ''")
			continue
		}

		if _, exists := queues.maps[name]; !exists {
			errorItemClose(conn, false, fmt.Sprintf("Unknown '%s' queue", name))
			continue
		}

		queue := queues.maps[name]

		queue.mutex.Lock()

		offset := countObsolete(queue)
		if offset > 0 {
			log.Printf("WARN: discarded %d requests from disappeared client(s)", offset)
			queue.disconnect += uint64(offset)
		}

		if offset >= len(queue.items) {
			errorItemClose(conn, true, fmt.Sprintf("Queue '%s' is empty", name))

			queue.items = []queueItem{}
		} else {
			// new goroutine for the connection
			go doItem(conn, queue.items[offset], queue)

			queue.items = queue.items[offset+1:]
			queue.running++
		}
		queue.mutex.Unlock()
	}
}

func listenPrometheus(addr string, queues *queuesT) {
	// Set both header and whole message timeout to same value
	// as handler rejects HTTP queries with a body
	server := &http.Server{
		Addr:              addr,
		ReadTimeout:       time.Second,
		ReadHeaderTimeout: time.Second,
		MaxHeaderBytes:    4096,
	}

	http.HandleFunc(metricURL, queues.exporter)
	log.Printf("Listening queue metric queries on %s%s", addr, metricURL)
	log.Fatal(server.ListenAndServe())
}

func main() {
	var interval, qmax int

	var caddr, maddr, waddr string

	log.Printf("%s %s", project, version)
	flag.StringVar(&caddr, "caddr", "localhost:9997", "Address to listen for client service requests")
	flag.IntVar(&interval, "interval", 0, "Log queue statistics at given interval in seconds (0=disabled)")
	flag.StringVar(&maddr, "maddr", "localhost:9998", "Address to listen for Prometheus metric queries")
	flag.StringVar(&waddr, "waddr", "localhost:9999", "Address to listen for worker work item requests")
	flag.IntVar(&qmax, "qmax", 0, "Max queue size after which requests are denied (0=unlimited)")
	flag.BoolVar(&verbose, "verbose", false, "Log all messages")
	flag.Parse()

	names := flag.Args()
	if len(names) == 0 {
		log.Fatal("ERROR: no queue names specified (as arguments)")
	}

	// use fixed set of queue names
	queues := queuesT{maps: make(map[string]*queueT), interval: interval}

	for _, name := range names {
		if name == "" {
			log.Fatal("ERROR: invalid queue name ''")
		}

		queues.maps[name] = &queueT{
			mutex: sync.Mutex{},
			items: make([]queueItem, 0, qmax),
		}
	}

	log.Printf("Added queues: %v", names)

	// Go routines for handling input
	go queues.listenForClients(caddr, qmax)
	go queues.listenForWorkers(waddr)
	go listenPrometheus(maddr, &queues)

	// and automated stats logging
	if queues.interval > 0 {
		go queues.logStats()
	}

	// exit with 0 when asked nicely to terminate
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM)
	log.Printf("Got signal %d => terminating", <-sig)
	os.Exit(0)
}
