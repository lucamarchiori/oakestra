package virtualization

import (
	"errors"
	"fmt"
	"go_node_engine/logger"
	"go_node_engine/model"
	"os"
	"reflect"
	"sync"
	"time"

	wasmtime "github.com/bytecodealliance/wasmtime-go/v25"
	"github.com/struCoder/pidusage"
)

type WasmRuntime struct {
	killQueue   map[string]*chan bool
	channelLock *sync.RWMutex
}

var wasmRuntime = WasmRuntime{
	channelLock: &sync.RWMutex{},
}

var wasmSingletonOnce sync.Once

func GetWasmRuntime() *WasmRuntime {
	logger.InfoLogger().Print("Getting WASM runtime")
	wasmSingletonOnce.Do(func() {
		wasmRuntime.killQueue = make(map[string]*chan bool)
		model.GetNodeInfo().AddSupportedTechnology(model.WASM_RUNTIME)
	})
	return &wasmRuntime
}

func (r *WasmRuntime) StopWasmRuntime() {
	logger.InfoLogger().Print("Stopping WASM runtime")
	r.channelLock.Lock()
	taskIDs := reflect.ValueOf(r.killQueue).MapKeys()
	r.channelLock.Unlock()

	for _, taskid := range taskIDs {
		// Attempt to undeploy each service
		err := r.Undeploy(extractSnameFromTaskID(taskid.String()), extractInstanceNumberFromTaskID(taskid.String()))
		if err != nil {
			logger.ErrorLogger().Printf("Unable to undeploy %s, error: %v", taskid.String(), err)
		}
	}
	logger.InfoLogger().Print("WASM runtime stopped")
}

func (r *WasmRuntime) Deploy(service model.Service, statusChangeNotificationHandler func(service model.Service)) error {

	killChannel := make(chan bool, 1)
	startupChannel := make(chan bool, 1)
	errorChannel := make(chan error, 1)

	r.channelLock.RLock()
	el, servicefound := r.killQueue[genTaskID(service.Sname, service.Instance)]
	r.channelLock.RUnlock()
	if !servicefound || el == nil {
		r.channelLock.Lock()
		r.killQueue[genTaskID(service.Sname, service.Instance)] = &killChannel
		r.channelLock.Unlock()
	} else {
		return errors.New("Service already deployed")
	}

	logger.InfoLogger().Print("Deploying WASM service...")
	go r.WasmRuntimeCreationRoutine(service, &killChannel, startupChannel, errorChannel, statusChangeNotificationHandler)

	// Wait for the startup process
	success := <-startupChannel
	if !success {
		err := <-errorChannel
		return err
	}

	return nil
}

func (r *WasmRuntime) Undeploy(service string, instance int) error {
	r.channelLock.Lock()
	defer r.channelLock.Unlock()
	taskid := genTaskID(service, instance)
	el, found := r.killQueue[taskid]
	if found && el != nil {
		logger.InfoLogger().Printf("Sending kill signal to %s", taskid)
		*r.killQueue[taskid] <- true
		select {
		case res := <-*r.killQueue[taskid]:
			if res == false {
				logger.ErrorLogger().Printf("Unable to stop service %s", taskid)
			}
		case <-time.After(5 * time.Second):
			logger.ErrorLogger().Printf("Unable to stop service %s", taskid)
		}
		delete(r.killQueue, taskid)
		return nil
	}
	return errors.New("service not found")
}

func (r *WasmRuntime) WasmRuntimeCreationRoutine(
	service model.Service,
	killChannel *chan bool,
	startup chan bool,
	errorchan chan error,
	statusChangeNotificationHandler func(service model.Service),
) {
	taskid := genTaskID(service.Sname, service.Instance)
	// Update the service status to RUNNING
	service.Status = model.SERVICE_CREATED
	statusChangeNotificationHandler(service)

	revert := func(err error) {
		startup <- false
		errorchan <- err
		r.channelLock.Lock()
		defer r.channelLock.Unlock()
		r.killQueue[taskid] = nil
	}

	//codePath := service.Image // Assuming service.Image contains the path to the WASM module
	codePath := "/home/lucam/oak/oak-fork/go_node_engine/virtualization/wasm_test_modules/counting.wasm"
	entry := "_start" // Assuming the entry function is "_start"

	engcfg := wasmtime.NewConfig()
	engcfg.SetEpochInterruption(true)
	engine := wasmtime.NewEngineWithConfig(engcfg)
	defer engine.Close()

	code, err := os.ReadFile(codePath)
	if err != nil {
		revert(fmt.Errorf("error reading file %s: %v", codePath, err))
		return
	}

	// Set the log file
	logPath := fmt.Sprintf("%s/%s", model.GetNodeInfo().LogDirectory, taskid)
	file, err := os.OpenFile(logPath, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0644)
	if err != nil {
		revert(err)
		return
	}
	//defer file.Close()
	defer func() {
		if err := file.Close(); err != nil {
			logger.ErrorLogger().Printf("Unable to close log file: %v", err)
		}
	}()

	// Create a store with interruptable configuration
	store := wasmtime.NewStore(engine)
	defer store.Close()

	// When the epoch deadline is reached, the store will interrupt the execution of the module
	// this is used to send a kill signal to the module by incrementing the epoch later
	store.SetEpochDeadline(1)

	// Create a WASI configuration and set it in the store
	wasiConfig := wasmtime.NewWasiConfig()
	//wasiConfig.InheritStdout() // To inherit stdout for printing. Currently set to log file
	wasiConfig.SetStdoutFile(logPath) // Set the log file as stdout
	defer wasiConfig.Close()
	store.SetWasi(wasiConfig)

	// Compile the module
	module, err := wasmtime.NewModule(engine, code)
	if err != nil {
		revert(fmt.Errorf("error compiling module: %v", err))
		return
	}
	defer module.Close()
	logger.InfoLogger().Print("Compiled module")

	// Create a linker and link the WASI functions
	linker := wasmtime.NewLinker(engine)
	err = linker.DefineWasi()
	if err != nil {
		revert(fmt.Errorf("error defining WASI: %v", err))
		return
	}
	defer linker.Close()

	// Instantiate the module using the linker
	instance, err := linker.Instantiate(store, module)
	if err != nil {
		revert(fmt.Errorf("error instantiating module: %v", err))
		return
	}
	logger.InfoLogger().Print("Instantiated module")

	// Get the entry function
	run := instance.GetFunc(store, entry)
	if run == nil {
		revert(fmt.Errorf("function %s not found in the module", entry))
		return
	}

	// Indicate that startup was successful
	startup <- true

	// Run the function in a goroutine
	runResult := make(chan error, 1)
	go func() {
		_, err := run.Call(store)
		runResult <- err
	}()

	// Wait for the module to finish execution or kill signal
	select {
	case err := <-runResult:
		if err != nil {
			// Handle errors
			// In Wasmtime, the termination of execution calls the proc_exit function and raises
			// an exit exception to signal the termination of the program.
			// Filter errors of type *wasmtime.Error
			if exitErr, ok := err.(*wasmtime.Error); ok {
				exitCode, _ := exitErr.ExitStatus()
				if exitCode == 0 {
					logger.InfoLogger().Print("Program exited successfully with code 0")
					if service.OneShot {
						service.Status = model.SERVICE_COMPLETED
					} else {
						service.Status = model.SERVICE_DEAD
					}
					statusChangeNotificationHandler(service)
				} else {
					logger.InfoLogger().Printf("Program exited with code %d", exitCode)
					service.Status = model.SERVICE_DEAD
					statusChangeNotificationHandler(service)
				}
			} else {
				// Handle generic errors
				logger.InfoLogger().Printf("Error executing function '%s': %v", entry, err)
				service.Status = model.SERVICE_DEAD
				statusChangeNotificationHandler(service)
			}
		} else {
			logger.InfoLogger().Print("Module execution completed successfully")
			if service.OneShot {
				service.Status = model.SERVICE_COMPLETED
			} else {
				service.Status = model.SERVICE_DEAD
			}
			statusChangeNotificationHandler(service)
		}
	case <-*killChannel:
		logger.InfoLogger().Printf("Kill channel message received for WASM module %s", taskid)
		// Interrupt the execution of the module by incrementing the epoch
		engine.IncrementEpoch()
		// Wait for the module to respond to interrupt
		err := <-runResult
		if err != nil {
			// Handle errors of type *wasmtime.Trap
			if exitErr, ok := err.(*wasmtime.Trap); ok {
				logger.InfoLogger().Print(exitErr.Message())
				if exitErr.Code() != nil && *exitErr.Code() == wasmtime.Interrupt {
					logger.InfoLogger().Print("Module interrupted successfully")
				}
			} else {
				// Handle generic errors
				logger.InfoLogger().Printf("Error after interrupt: %v", err)
			}
		}

		// Update service status
		service.Status = model.SERVICE_DEAD
		statusChangeNotificationHandler(service)
	}

	// Clean up and notify that the routine is done
	*r.killQueue[taskid] <- true
	r.channelLock.Lock()
	delete(r.killQueue, taskid)
	r.channelLock.Unlock()
}

func (r *WasmRuntime) ResourceMonitoring(every time.Duration, notifyHandler func(res []model.Resources)) {
	for {
		select {
		case <-time.After(every):
			resourceList := make([]model.Resources, 0)
			r.channelLock.RLock()
			pid := os.Getpid()
			sysInfo, err := pidusage.GetStat(pid)
			if err != nil {
				logger.ErrorLogger().Printf("Unable to fetch task info: %v", err)
				continue
			}
			// Since WASM modules run in the same process, it's difficult to get per-module resource usage.
			//This shows statistics of the whole Node Engine process that runs the WASM runtime.
			for taskid := range r.killQueue {
				resourceList = append(resourceList, model.Resources{
					Cpu:      fmt.Sprintf("%f", sysInfo.CPU),
					Memory:   fmt.Sprintf("%f", sysInfo.Memory),
					Disk:     "0",
					Sname:    extractSnameFromTaskID(taskid),
					Logs:     getLogs(taskid),
					Runtime:  string(model.WASM_RUNTIME),
					Instance: extractInstanceNumberFromTaskID(taskid),
				})
			}
			r.channelLock.RUnlock()
			notifyHandler(resourceList)
		}
	}
}
