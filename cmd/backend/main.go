// Copyright 2023 Intel Corporation
// SPDX-License-Identifier: Apache-2.0
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	project = "Device Scalability Tester for Kubernetes - backend"
	version = "v0.1"
	mapster = "FILENAME"
	tcpSize = 1024 // max space for a TCP message
)

var verbose bool

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

// worker -> frontend -> client return replies.
type replyT struct {
	Pod     string  // who did work
	Node    string  // on which node
	Device  string  // device mapped to worker, if any
	Error   string  // non-empty on errors
	Timeout float64 // >0 = workload timed out
	Runtime float64 // workload run time, in secs
	Retcode int     // workload return code
}

// getEnv if env var name given, gets the value and if it's non-empty,
// returns that, otherwise fallback.
func getEnv(name, fallback string) string {
	if name == "" {
		return fallback
	}

	name = os.Getenv(name)
	if name == "" {
		return fallback
	}

	return name
}

// getFile() returns first file name matching the glob pattern.
// If no matches are found for non-empty pattern, process is terminated.
func getFile(glob string) string {
	if glob == "" {
		return ""
	}

	// available file name paths
	paths, err := filepath.Glob(glob)
	if paths == nil || err != nil {
		log.Fatalf("ERROR: no files matching glob pattern '%s'", glob)
	}

	if len(paths) > 1 {
		log.Printf("WARN: %d matches for glob pattern '%s'", len(paths), glob)
	}

	log.Printf("'%s' matches to '%s'", glob, path.Base(paths[0]))

	return paths[0]
}

// mapArgs maps "FILENAME" string in argument list to globbed file name.
// Returns modified slice and error string (if mapping failed).
func mapArgs(args []string, name string) ([]string, string) {
	for i, arg := range args {
		if !strings.Contains(arg, mapster) {
			continue
		}

		if name == "" {
			return nil, mapster + " in args, but no file name to map to it"
		}

		args[i] = strings.ReplaceAll(arg, mapster, name)
	}

	return args, ""
}

// getWork connects server, send work request, parses work item.  Returns
// connection and workItem, but when queue is empty, returned connection is nil.
func getWork(address string, req []byte, backoff bool) (net.Conn, workItem) {
	conn, err := net.Dial("tcp", address)
	if err != nil {
		log.Fatalf("ERROR: connection to '%s' failed: %v", address, err)
	}

	var n int

	n, err = conn.Write(req)
	if err != nil || n != len(req) {
		log.Fatalf("ERROR: request send write failed (%d/%d bytes): %v", n, len(req), err)
	}

	buf := make([]byte, tcpSize)

	n, err = conn.Read(buf)
	if err != nil {
		log.Fatalf("ERROR: request reply read failed: %v", err)
	}

	if verbose {
		log.Printf("Received (%d bytes) work item (or error): %v", n, string(buf))
	}

	item := workItem{}
	if err = json.Unmarshal(buf[:n], &item); err != nil {
		log.Fatalf("ERROR: JSON work item unmarshaling failed: %v", err)
	}

	if item.Error != "" {
		if item.Empty {
			if backoff {
				conn.Close()
				return nil, item
			}

			log.Printf("Terminating: %s", item.Error)
			os.Exit(0)
		}

		log.Fatalf("ERROR: server returned error: %s", item.Error)
	}

	return conn, item
}

// runSleep sleeps seconds amount parsed from args, until limit.
// Parsing the first arg instead of last one, allows backend invocation
// to override value specified (potentially) by the client requests.
// Returns retcode, timeout and error description (empty for no error).
func runSleep(args []string, limit float64) (int, float64, string) {
	if verbose {
		log.Printf("Run (limit=%.1fs): sleep %v", limit, args)
	}

	if len(args) == 0 {
		return 1, 0, "Sleep time (seconds) argument missing"
	}

	secs, err := strconv.ParseFloat(args[0], 64)
	if err != nil {
		return 1, 0, fmt.Sprintf("invalid sleep time value '%s': %v", args[0], err)
	}

	msg := ""
	retcode := 0
	timeout := 0.0

	if limit > 0.0 && secs > limit {
		msg = "Sleep timeout"
		timeout = limit
		secs = limit
	}

	time.Sleep(time.Duration(uint64(1000*secs)) * time.Millisecond)

	return retcode, timeout, msg
}

// runPath runs given binary with given args using given timelimit.
// Returns retcode, timeout and error description (empty for no error).
func runPath(args []string, attr *os.ProcAttr, limit float64) (int, float64, string) {
	if verbose {
		log.Printf("Run (limit=%.1fs): %v", limit, args)
	}

	path := args[0]
	proc, err := os.StartProcess(path, args, attr)

	if err != nil {
		log.Fatalf("ERROR: starting '%s' failed to: %v", path, err)
	}

	state, err := proc.Wait()
	if err != nil {
		log.Fatalf("ERROR: waiting '%s' failed to: %v", path, err)
	}

	// TODO: implement timeout handling
	timeout := 0.0
	retcode := state.ExitCode()
	msg := ""

	if retcode != 0 {
		msg = fmt.Sprintf("%s returned error code %d", path, retcode)
	}

	return retcode, timeout, msg
}

// doWork runs specified workload + args with given time limit.
// return reply struct of how it went.
func doWork(args []string, attr *os.ProcAttr, deflimit, limit float64) replyT {
	if limit <= 0.0 || limit > deflimit {
		limit = deflimit
	}

	var (
		msg     string
		retcode int
		timeout float64
	)

	start := time.Now()

	if args[0] == "sleep" {
		retcode, timeout, msg = runSleep(args[1:], limit)
	} else {
		retcode, timeout, msg = runPath(args, attr, limit)
	}

	runtime := time.Since(start).Seconds()

	log.Printf("%v = %d (%fs)", args, retcode, runtime)

	reply := replyT{
		Retcode: retcode,
		Timeout: timeout,
		Runtime: runtime,
		Error:   msg,
	}

	return reply
}

// sendReplyClose marshals + sends reply to given connection and closes it.
func sendReplyClose(conn net.Conn, reply replyT) {
	data, err := json.MarshalIndent(reply, "", "\t")
	if err != nil {
		log.Fatalf("ERROR: reply JSON marshaling failed: %v", err)
	}

	if verbose {
		log.Printf("Closing reply (%d bytes): %v", len(data), string(data))
	}

	var n int

	n, err = conn.Write(data)
	if err != nil || n != len(data) {
		log.Fatalf("ERROR: request send write failed (%d/%d bytes): %v", n, len(data), err)
	}

	conn.Close()
}

// getAttr sets workload workdir + output redirection to returned *struct.
func getAttr(dir string, nullin, nullout bool) *os.ProcAttr {
	var devnull, stdin, stdout, stderr *os.File

	if nullin || nullout {
		var err error

		devnull, err = os.OpenFile("/dev/null", os.O_WRONLY, 0644)
		if err != nil {
			log.Fatalf("ERROR: opening /dev/null failed with: %v", err)
		}
	}

	if nullin {
		stdin = devnull

		log.Print("stdin mapped to /dev/null")
	} else {
		stdin = os.Stdin

		log.Print("stdin mapped to parent stdin")
	}

	if nullout {
		stdout, stderr = devnull, devnull

		log.Print("stdout/stderr mapped to /dev/null")
	} else {
		stdout, stderr = os.Stdout, os.Stderr

		log.Print("stdout/stderr mapped to parent stdout/stderr")
	}

	return &os.ProcAttr{
		Files: []*os.File{stdin, stdout, stderr},
		Dir:   dir,
	}
}

// work for worker.
type workOptions struct {
	addr   string       // frontend service address
	file   string       // device file name
	node   string       // backend node name
	pod    string       // backend pod name
	args   []string     // workload arguments
	attr   *os.ProcAttr // workload process attributes
	req    []byte       // workload request data
	inc    float64      // queue poll backoff time increment
	max    float64      // queue poll backoff time max
	limit  float64      // workload runtime limit (secs)
	ignore bool         // ignore client provided extra workload args
	once   bool         // test: run workload directly & exit
}

func parseOptions() workOptions {
	opts := workOptions{}

	flag.StringVar(&opts.addr, "faddr", "localhost:9999", "Frontend service address:port for backend work queue")
	flag.Float64Var(&opts.inc, "backoff", 0, "When queue is empty, instead of exiting, retry again after N*backoff seconds, 0=disabled")
	flag.Float64Var(&opts.max, "backoff-max", 5, "Maximum backoff value in seconds")
	flag.Float64Var(&opts.limit, "limit", 0, "Backend workload invocation runtime limit in seconds, 0=none")
	flag.BoolVar(&opts.ignore, "ignore", false, "Ignore extra workload arguments provided in the client request")
	flag.BoolVar(&opts.once, "once", false, "Run command directly & exit (for command testing)")

	var dir, glob, name, nenv, penv string

	flag.StringVar(&dir, "dir", "", "Working directory for the backend workload")
	flag.StringVar(&glob, "glob", "", "Glob pattern for (device) file name(s), match replaces 'FILENAME' in work item args")
	flag.StringVar(&name, "name", "sleep", "Backend work items queue name")
	flag.StringVar(&nenv, "node-env", "", "Get reply node name from given variable instead of hostname")
	flag.StringVar(&penv, "pod-env", "", "Get reply pod name from given variable instead of hostname")

	var nullin, nullout bool

	flag.BoolVar(&nullin, "null-in", false, "Map workload stdin to /dev/null")
	flag.BoolVar(&nullout, "null-out", false, "Map workload stdout/stderr to /dev/null")
	flag.BoolVar(&verbose, "verbose", false, "Log all messages")
	flag.Parse()

	// in kubernetes, hostname gives podname
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "localhost"
	}

	opts.pod = getEnv(penv, host)
	opts.node = getEnv(nenv, host)
	opts.file = getFile(glob)

	args, errstr := mapArgs(flag.Args(), opts.file)
	if errstr != "" {
		log.Fatalf("ERROR: %s", errstr)
	}

	if len(args) == 0 || args[0] == "" || (args[0][0] != '/' && args[0] != "sleep") {
		log.Fatalf("ERROR: invalid workload, either give its absolute path, or use 'sleep', not: %s", args)
	}

	log.Printf("Node '%s' backend pod '%s' workload is: %s", opts.node, opts.pod, args)
	opts.args = args

	if opts.limit > 0 {
		log.Printf("With %.1fs run-time limit enforced", opts.limit)
	}

	if opts.inc < 0 || opts.max < opts.inc {
		log.Fatalf("ERROR: invalid backoff/-max values (0 <= %.1f < %.1f)", opts.inc, opts.max)
	}

	log.Printf("Sending '%s' queue work requests to '%s'", name, opts.addr)

	// all work item requests are identical
	opts.req, err = json.MarshalIndent(workReq{Queue: name}, "", "\t")
	if err != nil {
		log.Fatalf("ERROR: work request JSON marshaling failed: %v", err)
	}

	log.Printf("Work requests are identical (but use separate connections): %v", string(opts.req))

	// workload workdir + output redirection
	opts.attr = getAttr(dir, nullin, nullout)

	return opts
}

func main() {
	log.Printf("%s %s", project, version)

	opts := parseOptions()

	if opts.once {
		log.Print("Running command directly (-once)")
		doWork(opts.args, opts.attr, opts.limit, opts.limit)

		return
	}

	ch := make(chan os.Signal, 1)
	// catch user and k8s interrupts to exit gracefully
	signal.Notify(ch, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM)

	total := opts.inc
	completed := 0

	for {
		conn, item := getWork(opts.addr, opts.req, (opts.inc > 0))
		if conn == nil {
			if total > opts.max {
				total = opts.max
			}

			if opts.limit > 0.0 && total > opts.limit {
				total = opts.limit
			}

			log.Printf("Queue empty -> sleeping %.1fs", total)
			time.Sleep(time.Duration(uint64(1000*total)) * time.Millisecond)

			total += opts.inc
		} else {
			total = opts.inc
			var reply replyT

			if opts.ignore {
				reply = doWork(opts.args, opts.attr, item.Limit, opts.limit)
			} else {
				// need to append mapped args from client request to workload
				if reqargs, errstr := mapArgs(item.Args, opts.file); errstr == "" {
					allargs := append(opts.args, reqargs...)
					reply = doWork(allargs, opts.attr, item.Limit, opts.limit)
				} else {
					reply = replyT{Error: errstr}
				}
			}
			// add backend info
			reply.Node, reply.Pod = opts.node, opts.pod
			reply.Device = path.Base(opts.file)

			sendReplyClose(conn, reply)
			completed++
		}

		select {
		case sig := <-ch:
			log.Printf("Got %v signal while completing request %d / waiting next => terminating", sig, completed)
			return
		default:
			break
		}
	}
}
