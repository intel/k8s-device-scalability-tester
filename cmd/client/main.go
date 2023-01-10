// Copyright 2023 Intel Corporation
// SPDX-License-Identifier: Apache-2.0
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"html"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type outputType int

const (
	project = "Device Scalability Tester for Kubernetes - client"
	version = "v0.1"
	maxcols = 60
	tcpSize = 1024 // max space for a TCP message
	// different output types.
	plainOutput = outputType(0)
	htmlOutput  = outputType(0)
)

// client service request.
type clientReq struct {
	Queue string   // queue name
	Args  []string // extra workload arguments
	Limit float64  // workload run-time limit, in secs (0=default)
}

// worker -> frontend -> client return replies.
type replyT struct {
	Pod      string  // who did work
	Node     string  // on which node
	Device   string  // device mapped to worker, if any
	Error    string  // non-empty on errors
	Timeout  float64 // >0 = workload timed out
	Waittime float64 // queue wait time, in secs, added by frontend
	Runtime  float64 // workload run time, in secs
	Retcode  int     // workload return code
}

type replyStatT struct {
	success, failure uint64
}

// all time stats are for successful replies.
type timeStatT struct {
	// total is for calculating averages
	min, max, total float64
}

// metrics for generating per-node histograms.
type nodeStatT struct {
	device  map[string]uint64 // per dev replies
	pod     map[string]uint64 // per pod replies
	error   map[string]uint64 // received errors
	runtime []float64         // run times for all replies
	reply   replyStatT
}

// to convert unsorted map[string]uint64 to a sorted []statCountT list.
type statCountT struct {
	name  string
	count uint64
}

type statsT struct {
	node map[string]*nodeStatT
	// processing info for requests
	start   time.Time
	reply   replyStatT
	pending uint64 // number of in-flight requests
	// total timings for requests
	run  timeStatT // backend run time
	wait timeStatT // queue wait time
	comm timeStatT // request completion time (diff to wait + run)
	// HTTP endpoint statistics, never reseted after startup
	completed uint64
	rejected  uint64
	// locking for those
	mutex sync.Mutex
}

var (
	stats   statsT // request statistics
	verbose bool   // verbose messaging
	// set request parallelization: 0 <= x <= reqmax.
	parallel chan int = make(chan int, 1)
)

// map2slice converts given string->uint64 mapping to a nameCounT list for sorting.
func map2slice(mapping map[string]uint64) []statCountT {
	slice := make([]statCountT, len(mapping))
	i := 0

	for name, count := range mapping {
		slice[i] = statCountT{name, count}
		i++
	}

	return slice
}

// sortByName converts given string->uint64 mapping to a count-sorted list.
func sortByName(mapping map[string]uint64) []statCountT {
	slice := map2slice(mapping)
	sort.Slice(slice, func(i, j int) bool {
		return slice[i].name > slice[j].name
	})

	return slice
}

// sortByCount converts given string->uint64 mapping to a name-sorted list.
func sortByCount(mapping map[string]uint64) []statCountT {
	slice := map2slice(mapping)
	sort.Slice(slice, func(i, j int) bool {
		return slice[i].count > slice[j].count
	})

	return slice
}

func printHeader(w io.Writer, title string) {
	fmt.Fprintf(w, "\n\n%s\n%s\n", title, strings.Repeat("=", len(title)))
}

func printHistHeader(w io.Writer, leftlen int, left, right string) {
	fmt.Fprintf(w, "\n%*s | %s:\n", leftlen, left, right)
	fmt.Fprintf(w, "%s+%s\n", strings.Repeat("-", leftlen+1), strings.Repeat("-", len(right)+2))
}

func printHistLine(w io.Writer, maxlen int, name string, total, part uint64) {
	if total == 0 {
		fmt.Fprint(w, "None\n")
		return
	}

	percentage := 100.0 * float64(part) / float64(total)
	cols := strings.Repeat("#", int((total/2+maxcols*part)/total))

	fmt.Fprintf(w, "%*s | %s %.1f%% (%d)\n", maxlen, name, cols, percentage, part)
}

func getMaxlen(items map[string]uint64) int {
	max := 0
	for name := range items {
		if len(name) > max {
			max = len(name)
		}
	}

	return max
}

// printNodeHistogram prints given string->uint64 mapping as name-sorted histogram.
func printNodeHistogram(w io.Writer, name string, total uint64, mapping map[string]uint64) {
	if len(mapping) == 0 {
		return
	}

	maxlen := getMaxlen(mapping)
	sorted := sortByName(mapping)

	printHistHeader(w, maxlen, name, "Completed requests")

	for _, item := range sorted {
		printHistLine(w, maxlen, item.name, total, item.count)
	}
}

// printNodeErrors prints count-sorted "count: string" list
// (when percentage does not matter and strings are too long for histogram).
func printNodeErrors(w io.Writer, mapping map[string]uint64, output outputType) {
	if len(mapping) == 0 {
		return
	}

	fmt.Fprint(w, "\nNode errors (count / description):\n")

	errors := sortByCount(mapping)
	for _, err := range errors {
		if output == plainOutput {
			fmt.Fprintf(w, "- %d: %s\n", err.count, err.name)
		} else {
			fmt.Fprintf(w, "- %d: %s\n", err.count, html.EscapeString(err.name))
		}
	}
}

// printNodeStats prints statistics for all nodes, then per-node ones.
func printNodeStats(w io.Writer, output outputType) {
	printHeader(w, "Backend / worker node statistics")

	stats.mutex.Lock()
	defer stats.mutex.Unlock()

	if len(stats.node) == 0 {
		fmt.Fprintln(w, "No statistics for nodes")
		return
	}

	names := make([]string, len(stats.node))
	i, maxlen := 0, 0

	for name := range stats.node {
		if len(name) > maxlen {
			maxlen = len(name)
		}

		names[i] = name
		i++
	}

	printHistHeader(w, maxlen, "Node", "Success replies")
	sort.Strings(names)

	for _, name := range names {
		printHistLine(w, maxlen, name, stats.reply.success, stats.node[name].reply.success)
	}

	if stats.reply.failure > 0 {
		printHistHeader(w, maxlen, "Node", "Failure replies")

		total := uint64(0)

		for _, name := range names {
			printHistLine(w, maxlen, name, stats.reply.failure, stats.node[name].reply.failure)
			total += stats.node[name].reply.failure
		}

		if total == 0 {
			fmt.Fprintln(w, "No such replies => fails happened in communication, not running workloads")
		}
	}

	for _, name := range names {
		node := stats.node[name]
		printHeader(w, fmt.Sprintf("Node: %s", name))
		printNodeHistogram(w, "Device", node.reply.success, node.device)
		printNodeErrors(w, node.error, output)
	}
}

func nodeStatsOutput(w http.ResponseWriter, r *http.Request) bool {
	printNodeStats(w, htmlOutput)
	return true
}

func podStatsOutput(w http.ResponseWriter, r *http.Request) bool {
	printHeader(w, "Per-node backend / worker pod statistics")

	stats.mutex.Lock()
	defer stats.mutex.Unlock()

	if len(stats.node) == 0 {
		fmt.Fprintln(w, "No statistics for nodes")
		return true
	}

	for name, node := range stats.node {
		printHeader(w, fmt.Sprintf("Node: %s", name))
		printNodeHistogram(w, "Pod", node.reply.success, node.pod)
	}

	return true
}

func printMaxAvgMin(w io.Writer, count float64, t timeStatT, f string) {
	fmt.Fprintf(w, f, t.max, t.total/count, t.min)
}

func printStats(w io.Writer) {
	printHeader(w, "Overall request / reply statistics")

	stats.mutex.Lock()
	defer stats.mutex.Unlock()

	secs := time.Since(stats.start).Seconds()
	fmt.Fprintf(w, "\n%d pending, %d failed and %d successful requests in %.1f seconds.\n",
		stats.pending, stats.reply.failure, stats.reply.success, secs)

	if stats.reply.success == 0 {
		fmt.Fprintf(w, "\nHTTP queries: %d completed, %d rejected in total\n",
			stats.completed, stats.rejected)
		return
	}

	count := float64(stats.reply.success)
	fmt.Fprintf(w, "= %.2f successfully completed requests / second.\n", count/secs)

	fmt.Fprint(w, "\nMax / average / min timings (in seconds, for successful requests):\n")
	printMaxAvgMin(w, count, stats.run, "- %.1f / %.1f / %.1f - backend run time\n")
	printMaxAvgMin(w, count, stats.wait, "- %.1f / %.1f / %.1f - queue wait time\n")
	printMaxAvgMin(w, count, stats.comm, "- %.3f / %.3f / %.3f - communication overhead\n")

	fmt.Fprintf(w, "\nHTTP queries: %d completed, %d rejected in total\n",
		stats.completed, stats.rejected)
}

func statsOutput(w http.ResponseWriter, r *http.Request) bool {
	if r.FormValue("type") == "plain" {
		printStats(w)
		return true
	}

	fmt.Fprint(w,
		"<h1>Scalability tester client</h1>\n",
		"\n",
		"<h2>Request changes</h2>\n",
		"<form action=\"parallel\" method=\"get\"><label>Requests in parallel: ",
		"<input name=\"value\"></label><button>Change</button>",
		"</form>\n",
		"<form action=\"reset\" method=\"get\"><button>Reset stat metrics</button></form>\n",
		"\n",
		"<h2>Request statistics</h2>\n",
		"<p><a href=\"pods\">Per-node histograms of used backend Pods</a> (for debugging)\n",
		"<p><a href=\"nodes\">Per-node reply histograms</a>\n",
		"<form action=\"stats\" method=\"get\"><button>Refresh overall stats</button></form>\n",
		"\n")

	fmt.Fprintln(w, "<pre>")
	printStats(w)
	fmt.Fprintln(w, "</pre>")

	return true
}

func returnResult(w io.Writer, r *http.Request, result, info string) {
	switch r.FormValue("type") {
	case "plain":
		fmt.Fprintf(w, "%s\n", result)

	case "json":
		if len(result) > 0 && result[0] >= '0' && result[0] <= '9' {
			// numerical value
			fmt.Fprintf(w, "{\"result\": %s, \"info\": \"%s\"}", result, info)
		} else {
			fmt.Fprintf(w, "{\"result\": \"%s\", \"info\": \"%s\"}", result, info)
		}

	default: // html
		if info != "" {
			info = ", " + info
		}

		fmt.Fprintf(w, "<p>%s%s. <a href=\"stats\">back to stats</a>.</p>\n", result, info)
	}
}

func statsReset(w http.ResponseWriter, r *http.Request) bool {
	log.Printf("Stats reset request from '%s'", r.RemoteAddr)
	printStats(os.Stderr)

	stats.mutex.Lock()
	stats.pending = 0
	stats.start = time.Now()
	stats.node = make(map[string]*nodeStatT)
	stats.reply = replyStatT{}
	stats.run = timeStatT{}
	stats.wait = timeStatT{}
	stats.comm = timeStatT{}
	stats.mutex.Unlock()

	returnResult(w, r, "Reseted", "")
	log.Println("=> Reseted")

	return true
}

// failCount outputs to http client just the requests failure reply count.
func failCount(w http.ResponseWriter, r *http.Request) bool {
	stats.mutex.Lock()
	count := stats.reply.failure
	stats.mutex.Unlock()

	returnResult(w, r, fmt.Sprintf("%d", count), "request failures")

	return true
}

// reqsPerSecond outputs to http client just the requests-per-second value.
func reqsPerSecond(w http.ResponseWriter, r *http.Request) bool {
	stats.mutex.Lock()
	count := float64(stats.reply.success)
	secs := time.Since(stats.start).Seconds()
	stats.mutex.Unlock()

	rps := 0.0
	if secs > 0.0 {
		rps = count / secs
	}

	returnResult(w, r, fmt.Sprintf("%f", rps), "requests per second")

	return true
}

func parallelization(w http.ResponseWriter, r *http.Request) bool {
	log.Printf("Parallelization request from '%s', for '%v'", r.RemoteAddr, r.URL)
	printStats(os.Stderr)

	value, err := strconv.ParseInt(r.FormValue("value"), 10, 64)
	if err != nil {
		returnResult(w, r, "ERROR", fmt.Sprintf("%v", err))
		return false
	}

	parallel <- int(value)
	returnResult(w, r, fmt.Sprintf("%d", <-parallel), "requests in parallel")

	return true
}

func requestCheck(r *http.Request) int {
	if r.Method != http.MethodGet {
		return http.StatusMethodNotAllowed
	}

	if r.Body != http.NoBody {
		return http.StatusBadRequest
	}

	return http.StatusOK
}

func increaseRejected() {
	stats.mutex.Lock()
	stats.rejected++
	stats.mutex.Unlock()
}

func increaseCompleted() {
	stats.mutex.Lock()
	stats.completed++
	stats.mutex.Unlock()
}

func myHandler(w http.ResponseWriter, r *http.Request) {
	if status := requestCheck(r); status != http.StatusOK {
		log.Printf("Bad HTTP request type from '%s' => %v", r.RemoteAddr, status)
		w.WriteHeader(status)
		increaseRejected()

		return
	}

	handlers := map[string]func(http.ResponseWriter, *http.Request) bool{
		"/fails":        failCount,
		"/nodes":        nodeStatsOutput,
		"/parallel":     parallelization,
		"/pods":         podStatsOutput,
		"/reqs-per-sec": reqsPerSecond,
		"/reset":        statsReset,
		"/stats":        statsOutput,
	}

	handler, found := handlers[r.URL.Path]
	if !found {
		log.Printf("Unrecognized HTTP '%s' URL request from '%s'", r.URL.Path, r.RemoteAddr)
		w.WriteHeader(http.StatusNotFound)
		increaseRejected()

		return
	}

	if verbose {
		log.Printf("'%s' query from '%s'", r.URL.Path, r.RemoteAddr)
	}

	if handler(w, r) {
		increaseCompleted()
	} else {
		increaseRejected()
	}
}

// getStatsNode adds given node to global stats, if one is missing,
// and returns pointer to it. Must be called with stats.mutex held.
func getStatsNode(name string) *nodeStatT {
	if _, exists := stats.node[name]; !exists {
		stats.node[name] = &nodeStatT{
			runtime: make([]float64, 0),
			device:  make(map[string]uint64),
			pod:     make(map[string]uint64),
			error:   make(map[string]uint64),
		}
	}

	return stats.node[name]
}

// statsFailure adds reply failure info to statistics.
func statsFailure(reply replyT) {
	stats.mutex.Lock()
	defer stats.mutex.Unlock()

	stats.pending--
	stats.reply.failure++

	if reply.Node != "" {
		node := getStatsNode(html.EscapeString(reply.Node))
		// error message is escaped only on HTML output as
		// there are valid reasons to have <> chars in it
		node.error[reply.Error]++
		node.reply.failure++
	}
}

// updateTime updates given timeStat min/max/total values.
func updateTime(t *timeStatT, secs float64) {
	if secs > t.max {
		t.max = secs
	}

	if secs < t.min {
		t.min = secs
	}

	t.total += secs
}

// statsSuccess adds reply success info to statistics.
// Timings info is updated only on success.
func statsSuccess(reply replyT, commtime float64) {
	stats.mutex.Lock()
	defer stats.mutex.Unlock()

	stats.pending--
	stats.reply.success++

	node := getStatsNode(html.EscapeString(reply.Node))
	node.reply.success++

	node.runtime = append(node.runtime, reply.Runtime)
	updateTime(&stats.run, reply.Runtime)
	updateTime(&stats.wait, reply.Waittime)
	updateTime(&stats.comm, commtime)

	dev := html.EscapeString(reply.Device)
	node.device[dev]++

	pod := html.EscapeString(reply.Pod)
	node.pod[pod]++
}

// statsStart is called on query start, to reset stats timer if
// this the first query, and to increase in-flight query count.
func statsStart(start time.Time) {
	stats.mutex.Lock()
	defer stats.mutex.Unlock()

	if stats.pending == 0 && stats.reply.success == 0 && stats.reply.failure == 0 {
		if verbose {
			log.Println("Stats tracking started")
		}

		stats.start = start
	}
	stats.pending++
}

// doRequest sends the request, unmarshals the reply, updates stats and
// returns "" on success, error string on failure.
func doRequest(conn net.Conn, req []byte) string {
	start := time.Now()

	statsStart(start)

	n, err := conn.Write(req)
	if err != nil || n != len(req) {
		return fmt.Sprintf("request send write failed (%d/%d bytes): %v", n, len(req), err)
	}

	buf := make([]byte, tcpSize)

	n, err = conn.Read(buf)
	if err != nil {
		return fmt.Sprintf("request reply read failed: %v", err)
	}

	if verbose {
		log.Printf("Received (%d bytes) reply (or error): %v", n, string(buf))
	}

	reply := replyT{}
	if err = json.Unmarshal(buf[:n], &reply); err != nil {
		return fmt.Sprintf("JSON replyT unmarshaling failed: %v", err)
	}

	if reply.Error != "" {
		statsFailure(reply)
		log.Printf("WARN: request received remote error: %s", reply.Error)

		return ""
	}

	// terminate also if successful replies do not include node name
	if reply.Node == "" {
		return "request reply misses node name"
	}

	commtime := time.Since(start).Seconds() - reply.Waittime - reply.Runtime

	if verbose {
		log.Printf("Message reply took %.2fs (wait) + %.2fs (run) + %.2fs (comm)",
			reply.Waittime, reply.Runtime, commtime)
	}

	statsSuccess(reply, commtime)

	return ""
}

// doRequests does new connection, sends given request, waits reply,
// closes connection, and repeats that.
func doRequests(t int, proceed <-chan bool, finished chan<- int, address string, req []byte) {
	for {
		<-proceed

		conn, err := net.Dial("tcp", address)
		if err != nil {
			log.Fatalf("ERROR: thread-%d connection to '%s' failed: %v", t, address, err)
		}

		if msg := doRequest(conn, req); msg != "" {
			log.Fatalf("ERROR: thread-%d '%s' request failed, %s", t, address, msg)
		}
		finished <- t

		conn.Close()
	}
}

// doParallelRequests createc request goroutines, handles their parallelization
// and never returns. Signal handler terminates the program on suitable signal.
func doParallelRequests(faddr string, data []byte, reqnow, reqmax int) {
	quit := make(chan os.Signal, 1)

	// graceful exit handling + stats output
	log.Println("SIGHUP/SIGINT/SIGTERM to terminate")
	signal.Notify(quit, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM)

	// create control channels or all possible requests
	proceed := make([]chan bool, reqmax)
	for i := 0; i < reqmax; i++ {
		proceed[i] = make(chan bool, 2)
	}

	// allow initial number of requests to proceed
	for i := 0; i < reqnow; i++ {
		proceed[i] <- true
	}

	// for notifying which request routine finished.
	// 'reqmax' sized to avoid worst-case parallel->proceed[i]->finished deadlock
	finished := make(chan int, reqmax)

	// start max amount of request handlers
	for i := 0; i < reqmax; i++ {
		go doRequests(i, proceed[i], finished, faddr, data)
	}

	// request parallelization main loop
	for {
		select {
		case i := <-finished:
			// let allowed requests to proceed
			if reqnow > i {
				proceed[i] <- true
			}

		case value := <-parallel:
			target := value
			if target > reqmax {
				target = reqmax
			} else if target < 0 {
				target = 0
			}
			// allow pending requests to proceed
			for i := reqnow; i < target; i++ {
				proceed[i] <- true
			}
			parallel <- target

			if target != value {
				log.Printf("Parallelization request %d constrained to %d", value, target)
			}

			log.Printf("=> Parallelization change: %d -> %d", reqnow, target)
			reqnow = target

		case sig := <-quit:
			log.Printf("Got %v signal -> terminating", sig)
			printNodeStats(os.Stdout, plainOutput)
			printStats(os.Stdout)
			os.Exit(0)
		}
	}
}

func main() {
	var (
		reqnow, reqmax     int
		caddr, faddr, name string
		limit              float64
	)

	log.Printf("%s %s", project, version)
	flag.StringVar(&caddr, "caddr", "localhost:9996", "Client query parallelization control + statistics reset / output")
	flag.StringVar(&faddr, "faddr", "localhost:9997", "Frontend service address:port for client requests")
	flag.Float64Var(&limit, "limit", 0.0, "backend runtime limit in seconds, 0=none")
	flag.StringVar(&name, "name", "sleep", "Service request queue name (client args are set to request as-is)")
	flag.IntVar(&reqmax, "req-max", 2, "Maximum number of parallel requests that can be specified at runtime")
	flag.IntVar(&reqnow, "req-now", 1, "Initial number of parallel requests")
	flag.BoolVar(&verbose, "verbose", false, "Log all messages")
	flag.Parse()

	if reqnow < 0 || reqnow > reqmax || reqmax > 512 {
		log.Fatalf("Invalid parallelization: 0 <= reqnow (%d) <= reqmax (%d) <= 512", reqnow, reqmax)
	}

	req := clientReq{
		Queue: name,
		Args:  flag.Args(),
		Limit: limit,
	}

	data, err := json.MarshalIndent(req, "", "\t")
	if err != nil {
		log.Fatalf("ERROR: client request JSON marshaling failed: %v", err)
	}

	log.Printf("Sending following requests to '%s' from %d parallel threads: %v", faddr, reqnow, string(data))
	log.Printf("Query parallelization, statistics reset and output on '%s'", caddr)

	// create stats before there are any threads
	stats.node = make(map[string]*nodeStatT)
	stats.start = time.Now()

	// HTTP handling
	http.HandleFunc("/", myHandler)
	// Set both header and whole message timeout to same value
	// as handler rejects HTTP queries with a body
	server := &http.Server{
		Addr:              caddr,
		ReadTimeout:       time.Second,
		ReadHeaderTimeout: time.Second,
		MaxHeaderBytes:    4096,
	}
	// HTTP query handling
	go func() { log.Fatal(server.ListenAndServe()) }()

	// create request goroutines & handle their parallelization
	doParallelRequests(faddr, data, reqnow, reqmax)
}
