/*
Copyright 2011-2017 Frederic Langlet
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
	"fmt"
	kanzi "github.com/flanglet/kanzi-go"
	kio "github.com/flanglet/kanzi-go/io"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	DECOMP_DEFAULT_BUFFER_SIZE = 32768
	DECOMP_DEFAULT_CONCURRENCY = 1
	DECOMP_MAX_CONCURRENCY     = 64
)

// Main block decompressor struct
type BlockDecompressor struct {
	verbosity  uint
	overwrite  bool
	inputName  string
	outputName string
	jobs       uint
	listeners  []kanzi.Listener
	cpuProf    string
}

type FileDecompressResult struct {
	code int
	read uint64
}

func NewBlockDecompressor(argsMap map[string]interface{}) (*BlockDecompressor, error) {
	this := new(BlockDecompressor)
	this.listeners = make([]kanzi.Listener, 0)

	if force, prst := argsMap["overwrite"]; prst == true {
		this.overwrite = force.(bool)
		delete(argsMap, "overwrite")
	} else {
		this.overwrite = false
	}

	this.inputName = argsMap["inputName"].(string)
	delete(argsMap, "inputName")
	this.outputName = argsMap["outputName"].(string)
	delete(argsMap, "outputName")
	concurrency := argsMap["jobs"].(uint)
	delete(argsMap, "jobs")
	this.verbosity = argsMap["verbose"].(uint)
	delete(argsMap, "verbose")

	if concurrency == 0 {
		this.jobs = DECOMP_DEFAULT_CONCURRENCY
	} else {
		if concurrency > DECOMP_MAX_CONCURRENCY {
			if this.verbosity > 0 {
				fmt.Printf("Warning: the number of jobs is too high, defaulting to %v\n", DECOMP_MAX_CONCURRENCY)
			}

			concurrency = DECOMP_MAX_CONCURRENCY
		}
		this.jobs = concurrency
	}

	if prof, prst := argsMap["cpuProf"]; prst == true {
		this.cpuProf = prof.(string)
		delete(argsMap, "cpuProf")
	} else {
		this.cpuProf = ""
	}

	if this.verbosity > 0 && len(argsMap) > 0 {
		for k, _ := range argsMap {
			log.Println("Ignoring invalid option ["+k+"]", this.verbosity > 0)
		}
	}

	return this, nil
}

func (this *BlockDecompressor) AddListener(bl kanzi.Listener) bool {
	if bl == nil {
		return false
	}

	this.listeners = append(this.listeners, bl)
	return true
}

func (this *BlockDecompressor) RemoveListener(bl kanzi.Listener) bool {
	for i, e := range this.listeners {
		if e == bl {
			this.listeners = append(this.listeners[:i-1], this.listeners[i+1:]...)
			return true
		}
	}

	return false
}

func (this *BlockDecompressor) CpuProf() string {
	return this.cpuProf
}

func fileDecompressWorker(tasks <-chan FileDecompressTask, cancel <-chan bool, results chan<- FileDecompressResult) {
	// Pull tasks from channel and run them
	more := true

	for more {
		select {
		case t, m := <-tasks:
			more = m

			if more {
				res, read := t.Call()
				results <- FileDecompressResult{code: res, read: read}
				more = res == 0
			}

		case c := <-cancel:
			more = !c
		}
	}
}

// Return exit code, number of bits written
func (this *BlockDecompressor) Call() (int, uint64) {
	var err error
	before := time.Now()
	files := make([]FileData, 0, 256)
	files, err = createFileList(this.inputName, files)

	if err != nil {
		if ioerr, isIOErr := err.(kio.IOError); isIOErr == true {
			fmt.Printf("%s\n", ioerr.Error())
			return ioerr.ErrorCode(), 0
		}

		fmt.Printf("An unexpected condition happened. Exiting ...\n%v\n", err.Error())
		return kanzi.ERR_OPEN_FILE, 0
	}

	if len(files) == 0 {
		fmt.Printf("Cannot open input file '%v'\n", this.inputName)
		return kanzi.ERR_OPEN_FILE, 0
	}

	nbFiles := len(files)
	printFlag := this.verbosity > 2
	var msg string

	if nbFiles > 1 {
		msg = fmt.Sprintf("%d files to decompress\n", nbFiles)
	} else {
		msg = fmt.Sprintf("%d file to decompress\n", nbFiles)
	}

	log.Println(msg, this.verbosity > 0)
	msg = fmt.Sprintf("Verbosity set to %v", this.verbosity)
	log.Println(msg, printFlag)
	msg = fmt.Sprintf("Overwrite set to %t", this.overwrite)
	log.Println(msg, printFlag)

	if this.jobs > 1 {
		msg = fmt.Sprintf("Using %d jobs", this.jobs)
		log.Println(msg, printFlag)

		if strings.ToUpper(this.outputName) == "STDOUT" {
			fmt.Println("Cannot output to STDOUT with multiple jobs")
			return kanzi.ERR_CREATE_FILE, 0
		}
	} else {
		log.Println("Using 1 job", printFlag)
	}

	// Limit verbosity level when files are processed concurrently
	if this.jobs > 1 && nbFiles > 1 && this.verbosity > 1 {
		log.Println("Warning: limiting verbosity to 1 due to concurrent processing of input files.\n", true)
		this.verbosity = 1
	}

	if this.verbosity > 2 {
		if listener, err := NewInfoPrinter(this.verbosity, DECODING, os.Stdout); err == nil {
			this.AddListener(listener)
		}
	}

	res := 1
	read := uint64(0)
	var inputIsDir bool
	formattedOutName := this.outputName
	formattedInName := this.inputName
	specialOutput := strings.ToUpper(formattedOutName) == "NONE" || strings.ToUpper(formattedOutName) == "STDOUT"

	fi, err := os.Stat(this.inputName)

	if err != nil {
		fmt.Printf("Cannot access %v\n", formattedInName)
		return kanzi.ERR_OPEN_FILE, 0
	}

	if fi.IsDir() {
		inputIsDir = true

		if formattedInName[len(formattedInName)-1] == '.' {
			formattedInName = formattedInName[0 : len(formattedInName)-1]
		}

		if formattedInName[len(formattedInName)-1] != os.PathSeparator {
			formattedInName = formattedInName + string([]byte{os.PathSeparator})
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
				formattedOutName = formattedOutName + string([]byte{os.PathSeparator})
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

	ctx := make(map[string]interface{})
	ctx["verbosity"] = this.verbosity
	ctx["overwrite"] = this.overwrite

	if nbFiles == 1 {
		oName := formattedOutName
		iName := files[0].Path

		if len(oName) == 0 {
			oName = iName + ".bak"
		} else if inputIsDir == true && specialOutput == false {
			oName = formattedOutName + iName[len(formattedInName):] + ".bak"
		}

		ctx["fileSize"] = files[0].Size
		ctx["inputName"] = iName
		ctx["outputName"] = oName
		ctx["jobs"] = this.jobs
		task := FileDecompressTask{ctx: ctx, listeners: this.listeners}

		res, read = task.Call()
	} else {
		// Create channels for task synchronization
		tasks := make(chan FileDecompressTask, nbFiles)
		results := make(chan FileDecompressResult, nbFiles)
		cancel := make(chan bool, 1)

		jobsPerTask := kanzi.ComputeJobsPerTask(make([]uint, nbFiles), this.jobs, uint(nbFiles))
		n := 0
		sort.Sort(FileCompareByName{data: files})

		for _, f := range files {
			iName := f.Path
			oName := formattedOutName

			if len(oName) == 0 {
				oName = iName + ".bak"
			} else if inputIsDir == true && specialOutput == false {
				oName = formattedOutName + iName[len(formattedInName):] + ".bak"
			}

			taskCtx := make(map[string]interface{})

			for k, v := range ctx {
				taskCtx[k] = v
			}

			taskCtx["fileSize"] = f.Size
			taskCtx["inputName"] = iName
			taskCtx["outputName"] = oName
			taskCtx["jobs"] = jobsPerTask[n]
			n++
			task := FileDecompressTask{ctx: taskCtx, listeners: this.listeners}

			// Push task to channel. The workers are the consumers.
			tasks <- task
		}

		close(tasks)

		// Create one worker per job. A worker calls several tasks sequentially.
		for j := uint(0); j < this.jobs; j++ {
			go fileDecompressWorker(tasks, cancel, results)
		}

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
		msg = fmt.Sprintf("Total decoding time: %d ms", delta)
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

func bd_notifyListeners(listeners []kanzi.Listener, evt *kanzi.Event) {
	defer func() {
		if r := recover(); r != nil {
			// Ignore exceptions in listeners
		}
	}()

	for _, bl := range listeners {
		bl.ProcessEvent(evt)
	}
}

type FileDecompressTask struct {
	ctx       map[string]interface{}
	jobs      uint
	listeners []kanzi.Listener
}

func (this *FileDecompressTask) Call() (int, uint64) {
	var msg string
	verbosity := this.ctx["verbosity"].(uint)
	inputName := this.ctx["inputName"].(string)
	outputName := this.ctx["outputName"].(string)
	printFlag := verbosity > 2
	log.Println("Input file name set to '"+inputName+"'", printFlag)
	log.Println("Output file name set to '"+outputName+"'", printFlag)
	overwrite := this.ctx["overwrite"].(bool)

	var output io.WriteCloser

	if strings.ToUpper(outputName) == "NONE" {
		output, _ = kio.NewNullOutputStream()
	} else if strings.ToUpper(outputName) == "STDOUT" {
		output = os.Stdout
	} else {
		var err error

		if output, err = os.OpenFile(outputName, os.O_RDWR, 0666); err == nil {
			// File exists
			if overwrite == false {
				fmt.Printf("File '%v' exists and the 'overwrite' command ", outputName)
				fmt.Println("line option has not been provided")
				output.Close()
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
					if err = os.MkdirAll(path.Dir(strings.Replace(outputName, "\\", "/", -1)), os.ModePerm); err == nil {
						output, err = os.Create(outputName)
					}
				}

				if err != nil {
					fmt.Printf("Cannot open output file '%v' for writing: %v\n", outputName, err)
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
	printFlag = verbosity > 1
	log.Println("\nDecoding "+inputName+" ...", printFlag)
	log.Println("", verbosity > 3)
	var input io.ReadCloser

	if len(this.listeners) > 0 {
		evt := kanzi.NewEvent(kanzi.EVT_DECOMPRESSION_START, -1, 0, 0, false, time.Now())
		bd_notifyListeners(this.listeners, evt)
	}

	if strings.ToUpper(inputName) == "STDIN" {
		input = os.Stdin
	} else {
		var err error

		if input, err = os.Open(inputName); err != nil {
			fmt.Printf("Cannot open input file '%v': %v\n", inputName, err)
			return kanzi.ERR_OPEN_FILE, uint64(read)
		}

		defer func() {
			input.Close()
		}()
	}

	cis, err := kio.NewCompressedInputStream(input, this.ctx)

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

	buffer := make([]byte, DECOMP_DEFAULT_BUFFER_SIZE)
	decoded := len(buffer)
	before := time.Now()

	// Decode next block
	for decoded == len(buffer) {
		if decoded, err = cis.Read(buffer); err != nil {
			if ioerr, isIOErr := err.(*kio.IOError); isIOErr == true {
				fmt.Printf("%s\n", ioerr.Message())
				return ioerr.ErrorCode(), uint64(read)
			}

			fmt.Printf("An unexpected condition happened. Exiting ...\n%v\n", err)
			return kanzi.ERR_PROCESS_BLOCK, uint64(read)
		}

		if decoded > 0 {
			_, err = output.Write(buffer[0:decoded])

			if err != nil {
				fmt.Printf("Failed to write decompressed block to file '%v': %v\n", outputName, err)
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
	log.Println("", verbosity > 1)

	if delta >= 100000 {
		msg = fmt.Sprintf("%.1f s", float64(delta)/1000)
	} else {
		msg = fmt.Sprintf("%.0f ms", float64(delta))
	}

	msg = fmt.Sprintf("Decoding:          %v", msg)
	log.Println(msg, printFlag)
	msg = fmt.Sprintf("Input size:        %d", cis.GetRead())
	log.Println(msg, printFlag)
	msg = fmt.Sprintf("Output size:       %d", read)
	log.Println(msg, printFlag)

	if delta >= 100000 {
		msg = fmt.Sprintf("%.1f s", float64(delta)/1000)
	} else {
		msg = fmt.Sprintf("%.0f ms", float64(delta))
	}

	msg = fmt.Sprintf("Decoding %v: %v => %v bytes in %v", inputName, cis.GetRead(), read, msg)
	log.Println(msg, verbosity == 1)

	if delta > 0 {
		msg = fmt.Sprintf("Throughput (KB/s): %d", ((read*int64(1000))>>10)/int64(delta))
		log.Println(msg, printFlag)
	}

	log.Println("", verbosity > 1)

	if len(this.listeners) > 0 {
		evt := kanzi.NewEvent(kanzi.EVT_DECOMPRESSION_END, -1, int64(cis.GetRead()), 0, false, time.Now())
		bd_notifyListeners(this.listeners, evt)
	}

	return 0, uint64(read)
}
