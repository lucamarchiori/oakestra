package virtualization

import (
	"context"
	"errors"
	"fmt"
	"go_node_engine/logger"
	"go_node_engine/model"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bytecodealliance/wasmtime-go/v25"
	"github.com/struCoder/pidusage"
)

type WasmRuntime struct {
	killQueue   map[string]*chan bool
	channelLock *sync.RWMutex
	modules     map[string]*WasmModuleInstance
	ctx         context.Context
}

type WasmModuleInstance struct {
	Sname    string
	Instance int
	Cancel   context.CancelFunc
	Pid      int
}

var wasmRuntime = WasmRuntime{
	channelLock: &sync.RWMutex{},
}

var wasmSingletonOnce sync.Once

func GetWasmRuntime() *WasmRuntime {
	wasmSingletonOnce.Do(func() {
		wasmRuntime.killQueue = make(map[string]*chan bool)
		wasmRuntime.modules = make(map[string]*WasmModuleInstance)
		wasmRuntime.ctx = context.Background()
		model.GetNodeInfo().AddSupportedTechnology(model.WASM_RUNTIME)
	})
	return &wasmRuntime
}

func (r *WasmRuntime) StopWasmRuntime() {
	r.channelLock.Lock()
	taskIDs := reflect.ValueOf(r.killQueue).MapKeys()
	r.channelLock.Unlock()

	for _, taskid := range taskIDs {
		err := r.Undeploy(extractWasmSnameFromTaskID(taskid.String()), extractWasmInstanceNumberFromTaskID(taskid.String()))
		if err != nil {
			logger.ErrorLogger().Printf("Unable to undeploy %s, error: %v", taskid.String(), err)
		}
	}
}

func (r *WasmRuntime) Deploy(service model.Service, statusChangeNotificationHandler func(service model.Service)) error {
	killChannel := make(chan bool, 1)
	startupChannel := make(chan bool, 0)
	errorChannel := make(chan error, 0)

	taskid := genWasmTaskID(service.Sname, service.Instance)

	r.channelLock.RLock()
	el, serviceFound := r.killQueue[taskid]
	r.channelLock.RUnlock()

	if !serviceFound || el == nil {
		r.channelLock.Lock()
		r.killQueue[taskid] = &killChannel
		r.channelLock.Unlock()
	} else {
		return errors.New("Service already deployed")
	}

	// Start the Wasm module execution routine
	go r.wasmModuleExecutionRoutine(
		r.ctx,
		service,
		startupChannel,
		errorChannel,
		&killChannel,
		statusChangeNotificationHandler,
	)

	// Wait for updates regarding the module creation
	if <-startupChannel != true {
		return <-errorChannel
	}

	return nil
}

func (r *WasmRuntime) Undeploy(service string, instance int) error {
	r.channelLock.Lock()
	defer r.channelLock.Unlock()
	taskid := genWasmTaskID(service, instance)
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

func (r *WasmRuntime) wasmModuleExecutionRoutine(
	ctx context.Context,
	service model.Service,
	startup chan bool,
	errorchan chan error,
	killChannel *chan bool,
	statusChangeNotificationHandler func(service model.Service),
) {
	taskid := genWasmTaskID(service.Sname, service.Instance)
	//hostname := fmt.Sprintf("instance-%d", service.Instance)

	revert := func(err error) {
		startup <- false
		errorchan <- err
		r.channelLock.Lock()
		defer r.channelLock.Unlock()
		r.killQueue[taskid] = nil
	}

	// Prepare the Wasm module
	wasmPath, err := r.getWasmModule(service.Image, service.Sname, service.Instance)
	if err != nil {
		logger.ErrorLogger().Printf("Error preparing Wasm module: %v", err)
		revert(err)
		return
	}

	engine := wasmtime.NewEngine()
	store := wasmtime.NewStore(engine)
	wasiConfig := wasmtime.NewWasiConfig()
	wasiConfig.InheritStdout() // To inherit stdout for printing
	wasiConfig.InheritStderr()
	wasiConfig.InheritStdin()
	store.SetWasi(wasiConfig)

	module, err := wasmtime.NewModuleFromFile(engine, wasmPath)
	if err != nil {
		logger.ErrorLogger().Printf("Error compiling Wasm module: %v", err)
		revert(err)
		return
	}

	linker := wasmtime.NewLinker(engine)
	err = linker.DefineWasi()
	if err != nil {
		logger.ErrorLogger().Printf("Error defining WASI: %v", err)
		revert(err)
		return
	}

	instance, err := linker.Instantiate(store, module)
	if err != nil {
		logger.ErrorLogger().Printf("Error instantiating Wasm module: %v", err)
		revert(err)
		return
	}

	entryFunc := "_start"
	if len(service.Commands) > 0 {
		entryFunc = service.Commands[0]
	}

	run := instance.GetFunc(store, entryFunc)
	if run == nil {
		err = fmt.Errorf("function %s not found in the module", entryFunc)
		logger.ErrorLogger().Println(err)
		revert(err)
		return
	}

	// Create a context to manage cancellation
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start execution in a goroutine
	go func() {
		// Capture any panic to prevent application crash
		defer func() {
			if r := recover(); r != nil {
				logger.ErrorLogger().Printf("Recovered from panic: %v", r)
				service.Status = model.SERVICE_DEAD
				statusChangeNotificationHandler(service)
			}
		}()

		// Signal that the module has started
		startup <- true

		// Run the Wasm function
		_, err = run.Call(store)
		if err != nil {
			if exitErr, ok := err.(*wasmtime.Error); ok {
				exitCode, _ := exitErr.ExitStatus()
				if exitCode != 0 {
					logger.ErrorLogger().Printf("Wasm module exited with code %d", exitCode)
					service.StatusDetail = fmt.Sprintf("Module exited with code: %d", exitCode)
				} else {
					logger.InfoLogger().Printf("Wasm module exited successfully")
				}
			} else {
				logger.ErrorLogger().Printf("Error executing function '%s': %v", entryFunc, err)
				service.StatusDetail = fmt.Sprintf("Error executing function '%s': %v", entryFunc, err)
			}
		}

		// Update service status
		if service.OneShot {
			service.Status = model.SERVICE_COMPLETED
		} else {
			service.Status = model.SERVICE_DEAD
		}
		statusChangeNotificationHandler(service)
	}()

	// Store the module instance for resource monitoring
	r.channelLock.Lock()
	r.modules[taskid] = &WasmModuleInstance{
		Sname:    service.Sname,
		Instance: service.Instance,
		Cancel:   cancel,
		Pid:      os.Getpid(),
	}
	r.channelLock.Unlock()

	// Wait for kill signal
	select {
	case <-ctx.Done():
		// Context canceled
	case <-*killChannel:
		// Kill signal received
		cancel()
	}

	// Clean up
	r.channelLock.Lock()
	delete(r.modules, taskid)
	r.channelLock.Unlock()
	*killChannel <- true
}

func (r *WasmRuntime) getWasmModule(image string, sname string, instance int) (string, error) {
	// For simplicity, we assume the image is a file URL or local path.
	// In production, you may need to handle pulling from a registry.

	// Determine the local file path
	var wasmPath string
	if strings.HasPrefix(image, "file://") {
		wasmPath = strings.TrimPrefix(image, "file://")
	} else {
		wasmPath = image
	}

	// Check if the file exists
	info, err := os.Stat(wasmPath)
	if err != nil {
		return "", fmt.Errorf("failed to stat file %s: %w", wasmPath, err)
	}

	if info.IsDir() {
		return "", fmt.Errorf("%s is a directory, expected a Wasm module file", wasmPath)
	}

	return wasmPath, nil
}

func (r *WasmRuntime) ResourceMonitoring(every time.Duration, notifyHandler func(res []model.Resources)) {
	for {
		select {
		case <-time.After(every):
			resourceList := make([]model.Resources, 0)
			r.channelLock.RLock()
			for taskid, module := range r.modules {
				// Get CPU and memory stats based on pid
				sysInfo, err := pidusage.GetStat(module.Pid)
				if err != nil {
					logger.ErrorLogger().Printf("Unable to fetch task info: %v", err)
					continue
				}
				resourceList = append(resourceList, model.Resources{
					Cpu:      fmt.Sprintf("%f", sysInfo.CPU),
					Memory:   fmt.Sprintf("%f", sysInfo.Memory),
					Disk:     fmt.Sprintf("%d", 0),
					Sname:    module.Sname,
					Logs:     getWasmLogs(taskid),
					Runtime:  string(model.WASM_RUNTIME),
					Instance: module.Instance,
				})
			}
			r.channelLock.RUnlock()
			notifyHandler(resourceList)
		}
	}
}

// Helper functions (same as in ContainerRuntime)
func extractWasmSnameFromTaskID(taskid string) string {
	sname := taskid
	index := strings.LastIndex(taskid, ".instance")
	if index > 0 {
		sname = taskid[0:index]
	}
	return sname
}

func extractWasmInstanceNumberFromTaskID(taskid string) int {
	instance := 0
	separator := ".instance."
	index := strings.LastIndex(taskid, separator)
	if index > 0 {
		numberStr := taskid[index+len(separator):]
		number, err := strconv.Atoi(numberStr)
		if err == nil {
			instance = number
		}
	}
	return instance
}

func genWasmTaskID(sname string, instancenumber int) string {
	return fmt.Sprintf("%s.instance.%d", sname, instancenumber)
}

// Placeholder for getLogs function
func getWasmLogs(taskid string) string {
	// Implement log retrieval if needed
	return ""
}
