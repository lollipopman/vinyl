// Most of this is from gofmt:
// Copyright 2009 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"go/scanner"
	"io"
	"io/fs"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"strings"

	"github.com/lollipopman/vinyl"

	"golang.org/x/sync/semaphore"
)

var (
	// main operation modes
	list   = flag.Bool("l", false, "list files whose formatting differs from vinyl's")
	write  = flag.Bool("w", false, "write result to (source) file instead of stdout")
	doDiff = flag.Bool("d", false, "display diffs instead of rewriting files")

	// debugging
	cpuprofile = flag.String("cpuprofile", "", "write cpu profile to this file")
)

func usage() {
	fmt.Fprintf(os.Stderr, "usage: vinyl [flags] [path ...]\n")
	flag.PrintDefaults()
}

func isYamlFile(f fs.DirEntry) bool {
	// ignore non-Yaml files
	name := f.Name()
	return !strings.HasPrefix(name, ".") && strings.HasSuffix(name, ".yaml") && !f.IsDir()
}

// A sequencer performs concurrent tasks that may write output, but emits that
// output in a deterministic order.
type sequencer struct {
	maxWeight int64
	sem       *semaphore.Weighted   // weighted by input bytes (an approximate proxy for memory overhead)
	prev      <-chan *reporterState // 1-buffered
}

// newSequencer returns a sequencer that allows concurrent tasks up to maxWeight
// and writes tasks' output to out and err.
func newSequencer(maxWeight int64, out, err io.Writer) *sequencer {
	sem := semaphore.NewWeighted(maxWeight)
	prev := make(chan *reporterState, 1)
	prev <- &reporterState{out: out, err: err}
	return &sequencer{
		maxWeight: maxWeight,
		sem:       sem,
		prev:      prev,
	}
}

// exclusive is a weight that can be passed to a sequencer to cause
// a task to be executed without any other concurrent tasks.
const exclusive = -1

// Add blocks until the sequencer has enough weight to spare, then adds f as a
// task to be executed concurrently.
//
// If the weight is either negative or larger than the sequencer's maximum
// weight, Add blocks until all other tasks have completed, then the task
// executes exclusively (blocking all other calls to Add until it completes).
//
// f may run concurrently in a goroutine, but its output to the passed-in
// reporter will be sequential relative to the other tasks in the sequencer.
//
// If f invokes a method on the reporter, execution of that method may block
// until the previous task has finished. (To maximize concurrency, f should
// avoid invoking the reporter until it has finished any parallelizable work.)
//
// If f returns a non-nil error, that error will be reported after f's output
// (if any) and will cause a nonzero final exit code.
func (s *sequencer) Add(weight int64, f func(*reporter) error) {
	if weight < 0 || weight > s.maxWeight {
		weight = s.maxWeight
	}
	if err := s.sem.Acquire(context.TODO(), weight); err != nil {
		// Change the task from "execute f" to "report err".
		weight = 0
		f = func(*reporter) error { return err }
	}

	r := &reporter{prev: s.prev}
	next := make(chan *reporterState, 1)
	s.prev = next

	// Start f in parallel: it can run until it invokes a method on r, at which
	// point it will block until the previous task releases the output state.
	go func() {
		if err := f(r); err != nil {
			r.Report(err)
		}
		next <- r.getState() // Release the next task.
		s.sem.Release(weight)
	}()
}

// AddReport prints an error to s after the output of any previously-added
// tasks, causing the final exit code to be nonzero.
func (s *sequencer) AddReport(err error) {
	s.Add(0, func(*reporter) error { return err })
}

// GetExitCode waits for all previously-added tasks to complete, then returns an
// exit code for the sequence suitable for passing to os.Exit.
func (s *sequencer) GetExitCode() int {
	c := make(chan int, 1)
	s.Add(0, func(r *reporter) error {
		c <- r.ExitCode()
		return nil
	})
	return <-c
}

// A reporter reports output, warnings, and errors.
type reporter struct {
	prev  <-chan *reporterState
	state *reporterState
}

// reporterState carries the state of a reporter instance.
//
// Only one reporter at a time may have access to a reporterState.
type reporterState struct {
	out, err io.Writer
	exitCode int
}

// getState blocks until any prior reporters are finished with the reporter
// state, then returns the state for manipulation.
func (r *reporter) getState() *reporterState {
	if r.state == nil {
		r.state = <-r.prev
	}
	return r.state
}

// Warnf emits a warning message to the reporter's error stream,
// without changing its exit code.
func (r *reporter) Warnf(format string, args ...interface{}) {
	fmt.Fprintf(r.getState().err, format, args...)
}

// Write emits a slice to the reporter's output stream.
//
// Any error is returned to the caller, and does not otherwise affect the
// reporter's exit code.
func (r *reporter) Write(p []byte) (int, error) {
	return r.getState().out.Write(p)
}

// Report emits a non-nil error to the reporter's error stream,
// changing its exit code to a nonzero value.
func (r *reporter) Report(err error) {
	if err == nil {
		panic("Report with nil error")
	}
	st := r.getState()
	scanner.PrintError(st.err, err)
	st.exitCode = 2
}

func (r *reporter) ExitCode() int {
	return r.getState().exitCode
}

// If info == nil, we are formatting stdin instead of a file.
// If in == nil, the source is the contents of the file with the given filename.
func processFile(filename string, info fs.FileInfo, in io.Reader, r *reporter) error {
	if in == nil {
		var err error
		in, err = os.Open(filename)
		if err != nil {
			return err
		}
	}

	// Compute the file's size and read its contents with minimal allocations.
	//
	// If the size is unknown (or bogus, or overflows an int), fall back to
	// a size-independent ReadAll.
	var src []byte
	size := -1
	if info != nil && info.Mode().IsRegular() && int64(int(info.Size())) == info.Size() {
		size = int(info.Size())
		// Empty files are valid yaml, but we don't need to format them
		if size == 0 {
			return nil
		}
	}
	if size+1 > 0 {
		// If we have the FileInfo from filepath.WalkDir, use it to make
		// a buffer of the right size and avoid ReadAll's reallocations.
		//
		// We try to read size+1 bytes so that we can detect modifications: if we
		// read more than size bytes, then the file was modified concurrently.
		// (If that happens, we could, say, append to src to finish the read, or
		// proceed with a truncated buffer — but the fact that it changed at all
		// indicates a possible race with someone editing the file, so we prefer to
		// stop to avoid corrupting it.)
		src = make([]byte, size+1)
		n, err := io.ReadFull(in, src)
		if err != nil && err != io.ErrUnexpectedEOF {
			return err
		}
		if n < size {
			return fmt.Errorf("error: size of %s changed during reading (from %d to %d bytes)", filename, size, n)
		} else if n > size {
			return fmt.Errorf("error: size of %s changed during reading (from %d to >=%d bytes)", filename, size, len(src))
		}
		src = src[:n]
	} else {
		// The file is not known to be regular, so we don't have a reliable size for it.
		var err error
		src, err = io.ReadAll(in)
		if err != nil {
			return err
		}
	}

	res, err := vinyl.Format(filename, bytes.NewBuffer(src))
	if err != nil {
		return err
	}

	if !bytes.Equal(src, res) {
		// formatting has changed
		if *list {
			fmt.Fprintln(r, filename)
		}
		if *write {
			if info == nil {
				panic("-w should not have been allowed with stdin")
			}
			// make a temporary backup before overwriting original
			perm := info.Mode().Perm()
			bakname, err := backupFile(filename+".", src, perm)
			if err != nil {
				return err
			}
			err = os.WriteFile(filename, res, perm)
			if err != nil {
				os.Rename(bakname, filename)
				return err
			}
			err = os.Remove(bakname)
			if err != nil {
				return err
			}
		}
		if *doDiff {
			data, err := diffWithReplaceTempFile(src, res, filename)
			if err != nil {
				return fmt.Errorf("computing diff: %s", err)
			}
			fmt.Fprintf(r, "diff -u %s %s\n", filepath.ToSlash(filename+".orig"), filepath.ToSlash(filename))
			r.Write(data)
		}
	}

	if !*list && !*write && !*doDiff {
		_, err = r.Write(res)
	}

	return err
}

func main() {
	// Arbitrarily limit in-flight work to 2MiB times the number of threads.
	//
	// The actual overhead for the parse tree and output will depend on the
	// specifics of the file, but this at least keeps the footprint of the process
	// roughly proportional to GOMAXPROCS.
	maxWeight := (2 << 20) * int64(runtime.GOMAXPROCS(0))
	s := newSequencer(maxWeight, os.Stdout, os.Stderr)

	// call vinylMain in a separate function
	// so that it can use defer and have them
	// run before the exit.
	vinylMain(s)
	os.Exit(s.GetExitCode())
}

func vinylMain(s *sequencer) {
	flag.Usage = usage
	flag.Parse()

	if *cpuprofile != "" {
		f, err := os.Create(*cpuprofile)
		if err != nil {
			s.AddReport(fmt.Errorf("creating cpu profile: %s", err))
			return
		}
		defer f.Close()
		pprof.StartCPUProfile(f)
		defer pprof.StopCPUProfile()
	}

	args := flag.Args()
	if len(args) == 0 {
		if *write {
			s.AddReport(fmt.Errorf("error: cannot use -w with standard input"))
			return
		}
		s.Add(0, func(r *reporter) error {
			return processFile("<standard input>", nil, os.Stdin, r)
		})
		return
	}

	for _, arg := range args {
		switch info, err := os.Stat(arg); {
		case err != nil:
			s.AddReport(err)
		case !info.IsDir():
			// Non-directory arguments are always formatted.
			arg := arg
			s.Add(fileWeight(arg, info), func(r *reporter) error {
				return processFile(arg, info, nil, r)
			})
		default:
			// Directories are walked, ignoring non-Yaml files.
			err := filepath.WalkDir(arg, func(path string, f fs.DirEntry, err error) error {
				if err != nil || !isYamlFile(f) {
					return err
				}
				info, err := f.Info()
				if err != nil {
					s.AddReport(err)
					return nil
				}
				s.Add(fileWeight(path, info), func(r *reporter) error {
					return processFile(path, info, nil, r)
				})
				return nil
			})
			if err != nil {
				s.AddReport(err)
			}
		}
	}
}

func fileWeight(path string, info fs.FileInfo) int64 {
	if info == nil {
		return exclusive
	}
	if info.Mode().Type() == fs.ModeSymlink {
		var err error
		info, err = os.Stat(path)
		if err != nil {
			return exclusive
		}
	}
	if !info.Mode().IsRegular() {
		// For non-regular files, FileInfo.Size is system-dependent and thus not a
		// reliable indicator of weight.
		return exclusive
	}
	return info.Size()
}

func diffWithReplaceTempFile(b1, b2 []byte, filename string) ([]byte, error) {
	data, err := Diff("vinyl", b1, b2)
	if len(data) > 0 {
		return replaceTempFilename(data, filename)
	}
	return data, err
}

// replaceTempFilename replaces temporary filenames in diff with actual one.
//
// --- /tmp/vinyl316145376	2017-02-03 19:13:00.280468375 -0500
// +++ /tmp/vinyl617882815	2017-02-03 19:13:00.280468375 -0500
// ...
// ->
// --- path/to/file.yaml.orig	2017-02-03 19:13:00.280468375 -0500
// +++ path/to/file.yaml	2017-02-03 19:13:00.280468375 -0500
// ...
func replaceTempFilename(diff []byte, filename string) ([]byte, error) {
	bs := bytes.SplitN(diff, []byte{'\n'}, 3)
	if len(bs) < 3 {
		return nil, fmt.Errorf("got unexpected diff for %s", filename)
	}
	// Preserve timestamps.
	var t0, t1 []byte
	if i := bytes.LastIndexByte(bs[0], '\t'); i != -1 {
		t0 = bs[0][i:]
	}
	if i := bytes.LastIndexByte(bs[1], '\t'); i != -1 {
		t1 = bs[1][i:]
	}
	// Always print filepath with slash separator.
	f := filepath.ToSlash(filename)
	bs[0] = []byte(fmt.Sprintf("--- %s%s", f+".orig", t0))
	bs[1] = []byte(fmt.Sprintf("+++ %s%s", f, t1))
	return bytes.Join(bs, []byte{'\n'}), nil
}

const chmodSupported = runtime.GOOS != "windows"

// backupFile writes data to a new file named filename<number> with permissions perm,
// with <number randomly chosen such that the file name is unique. backupFile returns
// the chosen file name.
func backupFile(filename string, data []byte, perm fs.FileMode) (string, error) {
	// create backup file
	f, err := os.CreateTemp(filepath.Dir(filename), filepath.Base(filename))
	if err != nil {
		return "", err
	}
	bakname := f.Name()
	if chmodSupported {
		err = f.Chmod(perm)
		if err != nil {
			f.Close()
			os.Remove(bakname)
			return bakname, err
		}
	}

	// write data to backup file
	_, err = f.Write(data)
	if err1 := f.Close(); err == nil {
		err = err1
	}

	return bakname, err
}

// Returns diff of two arrays of bytes in diff tool format.
func Diff(prefix string, b1, b2 []byte) ([]byte, error) {
	f1, err := writeTempFile(prefix, b1)
	if err != nil {
		return nil, err
	}
	defer os.Remove(f1)

	f2, err := writeTempFile(prefix, b2)
	if err != nil {
		return nil, err
	}
	defer os.Remove(f2)

	cmd := "diff"
	if runtime.GOOS == "plan9" {
		cmd = "/bin/ape/diff"
	}

	data, err := exec.Command(cmd, "-u", f1, f2).CombinedOutput()
	if len(data) > 0 {
		// diff exits with a non-zero status when the files don't match.
		// Ignore that failure as long as we get output.
		err = nil
	}

	// If we are on Windows and the diff is Cygwin diff,
	// machines can get into a state where every Cygwin
	// command works fine but prints a useless message like:
	//
	//	Cygwin WARNING:
	//	  Couldn't compute FAST_CWD pointer.  This typically occurs if you're using
	//	  an older Cygwin version on a newer Windows.  Please update to the latest
	//	  available Cygwin version from https://cygwin.com/.  If the problem persists,
	//	  please see https://cygwin.com/problems.html
	//
	// Skip over that message and just return the actual diff.
	if len(data) > 0 && !bytes.HasPrefix(data, []byte("--- ")) {
		i := bytes.Index(data, []byte("\n--- "))
		if i >= 0 && i < 80*10 && bytes.Contains(data[:i], []byte("://cygwin.com/")) {
			data = data[i+1:]
		}
	}

	return data, err
}

func writeTempFile(prefix string, data []byte) (string, error) {
	file, err := ioutil.TempFile("", prefix)
	if err != nil {
		return "", err
	}
	_, err = file.Write(data)
	if err1 := file.Close(); err == nil {
		err = err1
	}
	if err != nil {
		os.Remove(file.Name())
		return "", err
	}
	return file.Name(), nil
}
