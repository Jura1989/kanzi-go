/*
Copyright 2011-2024 Frederic Langlet
Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
you may obtain a copy of the License at

                http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	kanzi "github.com/flanglet/kanzi-go/v2"
	internal "github.com/flanglet/kanzi-go/v2/internal"
	kio "github.com/flanglet/kanzi-go/v2/io"
)

const (
	_DECOMP_DEFAULT_BUFFER_SIZE = 32768
	_DECOMP_MAX_CONCURRENCY     = 64
	_DECOMP_NONE                = "NONE"
	_DECOMP_STDIN               = "STDIN"
	_DECOMP_STDOUT              = "STDOUT"
)

// BlockDecompressor main block decompressor struct
type BlockDecompressor struct {
	verbosity  uint
	overwrite  bool
	inputName  string
	outputName string
	jobs       uint
	from       int // start blovk
	to         int // end block
	listeners  []kanzi.Listener
	cpuProf    string
}

type fileDecompressResult struct {
	code int
	read uint64
}

// NewBlockDecompressor creates a new instance of BlockDecompressor given
// a map of argument name/value pairs.
func NewBlockDecompressor(argsMap map[string]any) (*BlockDecompressor, error) {
	this := &BlockDecompressor{}
	this.listeners = make([]kanzi.Listener, 0)

	if force, hasKey := argsMap["overwrite"]; hasKey == true {
		this.overwrite = force.(bool)
		delete(argsMap, "overwrite")
	} else {
		this.overwrite = false
	}

	this.inputName = argsMap["inputName"].(string)
	delete(argsMap, "inputName")

	if len(this.inputName) == 0 {
		this.inputName = _DECOMP_STDIN
	}

	this.outputName = argsMap["outputName"].(string)
	delete(argsMap, "outputName")

	if len(this.outputName) == 0 && this.inputName == _DECOMP_STDIN {
		this.outputName = _DECOMP_STDOUT
	}

	concurrency := argsMap["jobs"].(uint)
	delete(argsMap, "jobs")
	this.verbosity = argsMap["verbosity"].(uint)
	delete(argsMap, "verbosity")

	if v, hasKey := argsMap["from"]; hasKey == true {
		this.from = v.(int)
		delete(argsMap, "from")
	} else {
		this.from = -1
	}

	if v, hasKey := argsMap["to"]; hasKey == true {
		this.to = v.(int)
		delete(argsMap, "to")
	} else {
		this.to = -1
	}

	if concurrency == 0 {
		cores := runtime.NumCPU() / 2 // defaults to half the cores

		if cores > 0 {
			if cores > _COMP_MAX_CONCURRENCY {
				concurrency = _COMP_MAX_CONCURRENCY
			} else {
				concurrency = uint(cores)
			}
		} else {
			concurrency = 1
		}
	} else if concurrency > _DECOMP_MAX_CONCURRENCY {
		if this.verbosity > 0 {
			fmt.Printf("Warning: the number of jobs is too high, defaulting to %d\n", _DECOMP_MAX_CONCURRENCY)
		}

		concurrency = _DECOMP_MAX_CONCURRENCY
	}

	this.jobs = concurrency

	if prof, hasKey := argsMap["cpuProf"]; hasKey == true {
		this.cpuProf = prof.(string)
		delete(argsMap, "cpuProf")
	} else {
		this.cpuProf = ""
	}

	if this.verbosity > 0 && len(argsMap) > 0 {
		for k := range argsMap {
			log.Println("Warning: ignoring invalid option ["+k+"]", this.verbosity > 0)
		}
	}

	return this, nil
}

// AddListener adds an event listener to this decompressor.
// Returns true if the listener has been added.
func (this *BlockDecompressor) AddListener(bl kanzi.Listener) bool {
	if bl == nil {
		return false
	}

	this.listeners = append(this.listeners, bl)
	return true
}

// RemoveListener removes an event listener from this decompressor.
// Returns true if the listener has been removed.
func (this *BlockDecompressor) RemoveListener(bl kanzi.Listener) bool {
	for i, e := range this.listeners {
		if e == bl {
			this.listeners = append(this.listeners[:i-1], this.listeners[i+1:]...)
			return true
		}
	}

	return false
}

// CPUProf returns the name of the CPU profile data file (maybe be empty)
func (this *BlockDecompressor) CPUProf() string {
	return this.cpuProf
}

func fileDecompressWorker(tasks <-chan fileDecompressTask, cancel <-chan bool, results chan<- fileDecompressResult) {
	// Pull tasks from channel and run them
	more := true

	for more {
		select {
		case t, m := <-tasks:
			more = m

			if more {
				res, read := t.call()
				results <- fileDecompressResult{code: res, read: read}
				more = res == 0
			}

		case c := <-cancel:
			more = !c
		}
	}
}

// Decompress is the main function to decompress the files or files based on the
// input name provided at construction. Files may be processed concurrently
// depending on the number of jobs provided at construction.
// Returns exit code, number of bits read.
func (this *BlockDecompressor) Decompress() (int, uint64) {
	var err error
	before := time.Now()
	files := make([]FileData, 0, 256)
	nbFiles := 1
	var msg string
	isStdIn := strings.ToUpper(this.inputName) == _DECOMP_STDIN

	if isStdIn == false {
		suffix := string([]byte{os.PathSeparator, '.'})
		target := this.inputName
		isRecursive := len(target) <= 2 || target[len(target)-len(suffix):] != suffix

		if isRecursive == false {
			target = target[0 : len(target)-1]
		}

		files, err = createFileList(target, files, isRecursive, false, false)

		if err != nil {
			if ioerr, isIOErr := err.(kio.IOError); isIOErr == true {
				fmt.Printf("%s\n", ioerr.Error())
				return ioerr.ErrorCode(), 0
			}

			fmt.Printf("An unexpected condition happened. Exiting ...\n%s\n", err.Error())
			return kanzi.ERR_OPEN_FILE, 0
		}

		if len(files) == 0 {
			fmt.Printf("Cannot open input file '%s'\n", this.inputName)
			return kanzi.ERR_OPEN_FILE, 0
		}

		nbFiles = len(files)

		if nbFiles > 1 {
			msg = fmt.Sprintf("%d files to decompress\n", nbFiles)
		} else {
			msg = fmt.Sprintf("%d file to decompress\n", nbFiles)
		}

		log.Println(msg, this.verbosity > 0)
	}

	// Limit verbosity level when output is stdout
	if strings.ToUpper(this.outputName) == _DECOMP_STDOUT {
		this.verbosity = 0
	}

	// Limit verbosity level when files are processed concurrently
	if this.jobs > 1 && nbFiles > 1 && this.verbosity > 1 {
		log.Println("Warning: limiting verbosity to 1 due to concurrent processing of input files.\n", true)
		this.verbosity = 1
	}

	if this.verbosity > 2 {
		msg = fmt.Sprintf("Verbosity set to %d", this.verbosity)
		log.Println(msg, true)
		msg = fmt.Sprintf("Overwrite set to %t", this.overwrite)
		log.Println(msg, true)

		if this.jobs > 1 {
			msg = fmt.Sprintf("Using %d jobs", this.jobs)
			log.Println(msg, true)
		} else {
			log.Println("Using 1 job", true)
		}
	}

	upperOutputName := strings.ToUpper(this.outputName)
	isStdOut := upperOutputName == _DECOMP_STDOUT

	// Limit verbosity level when output is stdout
	// Logic is duplicated here to avoid dependency to Kanzi.go
	if isStdOut == true {
		this.verbosity = 0
	}

	// Limit verbosity level when files are processed concurrently
	if this.jobs > 1 && nbFiles > 1 && this.verbosity > 1 {
		log.Println("Warning: limiting verbosity to 1 due to concurrent processing of input files.\n", true)
		this.verbosity = 1
	}

	if this.verbosity > 2 {
		if listener, err2 := NewInfoPrinter(this.verbosity, DECODING, os.Stdout); err2 == nil {
			this.AddListener(listener)
		}
	}

	read := uint64(0)
	var inputIsDir bool
	formattedOutName := this.outputName
	formattedInName := this.inputName
	specialOutput := upperOutputName == _DECOMP_NONE || upperOutputName == _DECOMP_STDOUT

	if isStdIn == false {
		fi, err := os.Stat(formattedInName)

		if err != nil {
			fmt.Printf("Cannot access %s\n", formattedInName)
			return kanzi.ERR_OPEN_FILE, 0
		}

		if fi.IsDir() {
			inputIsDir = true

			if len(formattedInName) > 1 && formattedInName[len(formattedInName)-1] == '.' {
				formattedInName = formattedInName[0 : len(formattedInName)-1]
			}

			if formattedInName[len(formattedInName)-1] != os.PathSeparator {
				formattedInName += string([]byte{os.PathSeparator})
			}

			if len(formattedOutName) > 0 && specialOutput == false {
				fi, err = os.Stat(formattedOutName)

				if err != nil {
					fmt.Println("Output must be an existing directory (or 'NONE')")
					return kanzi.ERR_OPEN_FILE, 0
				}

				if !fi.IsDir() {
					fmt.Println("Output must be a directory (or 'NONE')")
					return kanzi.ERR_CREATE_FILE, 0
				}

				if formattedOutName[len(formattedOutName)-1] != os.PathSeparator {
					formattedOutName += string([]byte{os.PathSeparator})
				}
			}
		} else {
			inputIsDir = false

			if len(formattedOutName) > 0 && specialOutput == false {
				fi, err = os.Stat(formattedOutName)

				if err == nil && fi.IsDir() {
					fmt.Println("Output must be a file (or 'NONE')")
					return kanzi.ERR_CREATE_FILE, 0
				}
			}
		}
	}

	ctx := make(map[string]any)
	ctx["verbosity"] = this.verbosity
	ctx["overwrite"] = this.overwrite
	var res int

	if this.from >= 0 {
		ctx["from"] = this.from
	}

	if this.to >= 0 {
		ctx["to"] = this.to
	}

	if nbFiles == 1 {
		oName := formattedOutName
		iName := _COMP_STDIN

		if isStdIn == true {
			if len(oName) == 0 {
				oName = _COMP_STDOUT
			}
		} else {
			iName = files[0].FullPath
			ctx["fileSize"] = files[0].Size

			if len(oName) == 0 {
				oName = iName + ".bak"
			} else if inputIsDir == true && specialOutput == false {
				oName = formattedOutName + iName[len(formattedInName):] + ".bak"
			}
		}

		ctx["inputName"] = iName
		ctx["outputName"] = oName
		ctx["jobs"] = this.jobs
		task := fileDecompressTask{ctx: ctx, listeners: this.listeners}

		res, read = task.call()
	} else {
		// Create channels for task synchronization
		tasks := make(chan fileDecompressTask, nbFiles)
		results := make(chan fileDecompressResult, nbFiles)
		cancel := make(chan bool, 1)

		jobsPerTask, _ := internal.ComputeJobsPerTask(make([]uint, nbFiles), this.jobs, uint(nbFiles))
		sort.Sort(FileCompare{data: files, sortBySize: true})

		for i, f := range files {
			iName := f.FullPath
			oName := formattedOutName

			if len(oName) == 0 {
				oName = iName + ".bak"
			} else if inputIsDir == true && specialOutput == false {
				oName = formattedOutName + iName[len(formattedInName):] + ".bak"
			}

			taskCtx := make(map[string]any)

			for k, v := range ctx {
				taskCtx[k] = v
			}

			taskCtx["fileSize"] = f.Size
			taskCtx["inputName"] = iName
			taskCtx["outputName"] = oName
			taskCtx["jobs"] = jobsPerTask[i]
			task := fileDecompressTask{ctx: taskCtx, listeners: this.listeners}

			// Push task to channel. The workers are the consumers.
			tasks <- task
		}

		close(tasks)

		// Create one worker per job. A worker calls several tasks sequentially.
		for j := uint(0); j < this.jobs; j++ {
			go fileDecompressWorker(tasks, cancel, results)
		}

		res = 0

		// Wait for all task results
		for i := 0; i < nbFiles; i++ {
			result := <-results
			read += result.read

			if result.code != 0 {
				// Exit early
				res = result.code
				break
			}
		}

		cancel <- true
		close(cancel)
		close(results)
	}

	after := time.Now()

	if nbFiles > 1 {
		delta := after.Sub(before).Nanoseconds() / 1000000 // convert to ms
		log.Println("", this.verbosity > 0)

		if delta >= 100000 {
			msg = fmt.Sprintf("%.1f s", float64(delta)/1000)
		} else {
			msg = fmt.Sprintf("%.0f ms", float64(delta))
		}

		msg = fmt.Sprintf("Total decompression time: %s", msg)
		log.Println(msg, this.verbosity > 0)

		if read > 1 {
			msg = fmt.Sprintf("Total output size: %d bytes", read)
		} else {
			msg = fmt.Sprintf("Total output size: %d byte", read)
		}

		log.Println(msg, this.verbosity > 0)
	}

	return res, read
}

func notifyBDListeners(listeners []kanzi.Listener, evt *kanzi.Event) {
	defer func() {
		//lint:ignore SA9003 Ignore panics in listeners
		if r := recover(); r != nil {
		}
	}()

	for _, bl := range listeners {
		bl.ProcessEvent(evt)
	}
}

type fileDecompressTask struct {
	ctx       map[string]any
	listeners []kanzi.Listener
}

func (this *fileDecompressTask) call() (int, uint64) {
	var msg string
	verbosity := this.ctx["verbosity"].(uint)
	inputName := this.ctx["inputName"].(string)
	outputName := this.ctx["outputName"].(string)

	if verbosity > 2 {
		log.Println("Input file name set to '"+inputName+"'", true)
		log.Println("Output file name set to '"+outputName+"'", true)
	}

	overwrite := this.ctx["overwrite"].(bool)

	var output io.WriteCloser

	if strings.ToUpper(outputName) == _DECOMP_NONE {
		output, _ = kio.NewNullOutputStream()
	} else if strings.ToUpper(outputName) == _DECOMP_STDOUT {
		output = os.Stdout
	} else {
		var err error

		if output, err = os.OpenFile(outputName, os.O_RDWR, 0666); err == nil {
			// File exists
			if overwrite == false {
				fmt.Printf("File '%s' exists and the 'force' command ", outputName)
				fmt.Println("line option has not been provided")
				return kanzi.ERR_OVERWRITE_FILE, 0
			}

			path1, _ := filepath.Abs(inputName)
			path2, _ := filepath.Abs(outputName)

			if path1 == path2 {
				fmt.Print("The input and output files must be different")
				return kanzi.ERR_CREATE_FILE, 0
			}
		} else {
			output, err = os.Create(outputName)

			if err != nil {
				if overwrite {
					// Attempt to create the full folder hierarchy to file
					if err = os.MkdirAll(path.Dir(strings.ReplaceAll(outputName, "\\", "/")), os.ModePerm); err == nil {
						output, err = os.Create(outputName)
					}
				}

				if err != nil {
					fmt.Printf("Cannot open output file '%s' for writing: %v\n", outputName, err)
					return kanzi.ERR_CREATE_FILE, 0
				}
			}
		}
	}

	defer func() {
		output.Close()
	}()

	// Decode
	read := int64(0)
	log.Println("\nDecompressing "+inputName+" ...", verbosity > 1)
	log.Println("", verbosity > 3)
	var input io.ReadCloser

	if len(this.listeners) > 0 {
		evt := kanzi.NewEvent(kanzi.EVT_DECOMPRESSION_START, -1, 0, 0, false, time.Now())
		notifyBDListeners(this.listeners, evt)
	}

	if strings.ToUpper(inputName) == _DECOMP_STDIN {
		input = os.Stdin
	} else {
		var err error

		if input, err = os.Open(inputName); err != nil {
			fmt.Printf("Cannot open input file '%s': %v\n", inputName, err)
			return kanzi.ERR_OPEN_FILE, uint64(read)
		}

		defer func() {
			input.Close()
		}()
	}

	cis, err := kio.NewReaderWithCtx(input, this.ctx)

	if err != nil {
		if err.(*kio.IOError) != nil {
			fmt.Printf("%s\n", err.(*kio.IOError).Message())
			return err.(*kio.IOError).ErrorCode(), uint64(read)
		}

		fmt.Printf("Cannot create compressed stream: %v\n", err)
		return kanzi.ERR_CREATE_DECOMPRESSOR, uint64(read)
	}

	for _, bl := range this.listeners {
		cis.AddListener(bl)
	}

	buffer := make([]byte, _DECOMP_DEFAULT_BUFFER_SIZE)
	decoded := len(buffer)
	before := time.Now()

	// Decode next block
	for decoded == len(buffer) {
		if decoded, err = cis.Read(buffer); err != nil {
			if ioerr, isIOErr := err.(*kio.IOError); isIOErr == true {
				fmt.Printf("%s\n", ioerr.Message())
				return ioerr.ErrorCode(), uint64(read)
			}

			if errors.Is(err, io.EOF) == false {
				// Ignore EOF (see comment in io.Copy:
				// Because Copy is defined to read from src until EOF, it does not
				// treat EOF from Read an an error to be reported)
				fmt.Printf("An unexpected condition happened. Exiting ...\n%v\n", err)
				return kanzi.ERR_PROCESS_BLOCK, uint64(read)
			}
		}

		if decoded > 0 {
			_, err = output.Write(buffer[0:decoded])

			if err != nil {
				fmt.Printf("Failed to write decompressed block to file '%s': %v\n", outputName, err)
				return kanzi.ERR_WRITE_FILE, uint64(read)
			}

			read += int64(decoded)
		}
	}

	// Close streams to ensure all data are flushed
	// Deferred close is fallback for error paths
	if err := cis.Close(); err != nil {
		fmt.Printf("%v\n", err)
		return kanzi.ERR_PROCESS_BLOCK, uint64(read)
	}

	after := time.Now()
	delta := after.Sub(before).Nanoseconds() / 1000000 // convert to ms

	if verbosity >= 1 {
		log.Println("", verbosity > 1)

		if delta >= 100000 {
			msg = fmt.Sprintf("%.1f s", float64(delta)/1000)
		} else {
			msg = fmt.Sprintf("%.0f ms", float64(delta))
		}

		if verbosity > 1 {
			msg = fmt.Sprintf("Decompressing:     %s", msg)
			log.Println(msg, true)
			msg = fmt.Sprintf("Input size:        %d", cis.GetRead())
			log.Println(msg, true)
			msg = fmt.Sprintf("Output size:       %d", read)
			log.Println(msg, true)
		}

		if verbosity == 1 {
			msg = fmt.Sprintf("Decompressing %s: %d => %d in %s", inputName, cis.GetRead(), read, msg)
			log.Println(msg, true)
		}

		if verbosity > 1 && delta > 0 {
			msg = fmt.Sprintf("Throughput (KB/s): %d", ((read*int64(1000))>>10)/delta)
			log.Println(msg, true)
		}

		log.Println("", verbosity > 1)
	}

	if len(this.listeners) > 0 {
		evt := kanzi.NewEvent(kanzi.EVT_DECOMPRESSION_END, -1, int64(cis.GetRead()), 0, false, time.Now())
		notifyBDListeners(this.listeners, evt)
	}

	return 0, uint64(read)
}
